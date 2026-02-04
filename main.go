package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/simonvetter/modbus"
)

// Define your target ranges here [StartRegister, EndRegister]
var inputRanges = [][]uint16{
	{0, 21},   // System info and Error bitmasks
	{30, 38},  // Temperatures, Humidity, and Fans
	{40, 52},  // Temperatures, Humidity, and Fans
	{60, 75},
	{100, 154}, // Wall sensor 2
	{160, 165}, // Alpha Panel 1
	{170, 175}, // Alpha Panel 2
	{180, 185}, // Alpha Panel 3
	{190, 195}, // Alpha Panel 4
	{200, 205}, // Alpha Panel 5
	{210, 215}, // Alpha Panel 6
	{220, 225}, // Alpha Panel 7
	{230, 235}, // Alpha Panel 8
}

var holdingRanges = [][]uint16{
	{0, 17},   // Modes, Timers, and User Settings
	{20, 23},
	{300, 305}, // external sensor 1
	{310, 315}, // external sensor 2
	{320, 325}, // external sensor 3
	{330, 335}, // external sensor 4
	{340, 345}, // external sensor 5
	{350, 355}, // external sensor 6
	{360, 365}, // external sensor 7
	{370, 375}, // external sensor 8
	{400, 403}, // external button 1
	{410, 413}, // external button 2
	{420, 423}, // external button 3
	{430, 433}, // external button 4
	{440, 443}, // external button 5
	{450, 453}, // external button 6
	{460, 463}, // external button 7
	{470, 473}, // external button 8
}

// Command-line options (defaults match previous constants)
var (
	flagUnitHost       = flag.String("host", "", "Modbus host or IP (required)")
	flagUnitPort       = flag.Uint("port", 502, "Modbus port")
	flagSlaveID        = flag.Uint("slave-id", 1, "Modbus slave ID (0-255)")
	flagMaxBlockSize   = flag.Uint("max-block-size", 125, "Max registers per Modbus read (standard limit is 125)")
	flagInputMaxAddr   = flag.Uint("input-max-addr", 255, "Max input register address for validation")
	flagHoldingMaxAddr = flag.Uint("holding-max-addr", 1024, "Max holding register address for validation")
	flagHTTPPort       = flag.Uint("http-port", 9090, "HTTP server port for metrics and UI")
)

//go:embed templates/edit.html
var editHTML string

//go:embed static/*
var staticFiles embed.FS

var editTmpl *template.Template
var runtimeMaxBlockSize uint16

func main() {
	flag.Parse()

	if *flagMaxBlockSize == 0 {
		log.Fatal("max-block-size must be greater than 0")
	}
	if *flagUnitHost == "" {
		log.Fatal("host is required")
	}
	if *flagMaxBlockSize > uint(^uint16(0)) {
		log.Fatalf("max-block-size %d exceeds uint16 max", *flagMaxBlockSize)
	}
	if *flagInputMaxAddr > uint(^uint16(0)) {
		log.Fatalf("input-max-addr %d exceeds uint16 max", *flagInputMaxAddr)
	}
	if *flagHoldingMaxAddr > uint(^uint16(0)) {
		log.Fatalf("holding-max-addr %d exceeds uint16 max", *flagHoldingMaxAddr)
	}
	if *flagSlaveID > 255 {
		log.Fatalf("slave-id %d exceeds uint8 max", *flagSlaveID)
	}

	validateRanges("input", inputRanges, uint16(*flagInputMaxAddr))
	validateRanges("holding", holdingRanges, uint16(*flagHoldingMaxAddr))

	if *flagUnitPort > uint(^uint16(0)) {
		log.Fatalf("port %d exceeds uint16 max", *flagUnitPort)
	}
	if *flagHTTPPort > 65535 {
		log.Fatalf("http-port %d exceeds 65535", *flagHTTPPort)
	}

	clientConfig := &modbus.ClientConfiguration{
		URL:     fmt.Sprintf("tcp://%s:%d", *flagUnitHost, *flagUnitPort),
		Timeout: 5 * time.Second,
	}

	client, err := modbus.NewClient(clientConfig)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	if err := client.SetUnitId(uint8(*flagSlaveID)); err != nil {
		log.Fatalf("Failed to set slave id: %v", err)
	}

	err = client.Open()
	if err != nil {
		log.Fatalf("Failed to connect: %v. Is another tool open?", err)
	}
	defer client.Close()

	// Parse embedded edit page template
	var errT error
	editTmpl, errT = template.New("edit").Parse(editHTML)
	if errT != nil {
		log.Fatalf("Failed to parse edit template: %v", errT)
	}

	// Register Prometheus metrics
	RegisterRegMetrics()

	// Start HTTP server for metrics, edit page, and write API
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/edit", handleEditPage)
	http.HandleFunc("/api/read-holding", handleReadHolding(client))
	http.HandleFunc("/api/read-input", handleReadInput(client))
	http.HandleFunc("/api/write-holding", handleWriteHolding(client))
	// Serve static assets (images, css, etc.) from embedded files
	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to access embedded static files: %v", err)
	}
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	httpAddr := fmt.Sprintf(":%d", *flagHTTPPort)
	go func() {
		log.Printf("Starting HTTP server on %s", httpAddr)
		if err := http.ListenAndServe(httpAddr, nil); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Polling loop: read input and holding ranges periodically and update metrics
	pollInterval := 5 * time.Second
	runtimeMaxBlockSize = uint16(*flagMaxBlockSize)
	for range time.Tick(pollInterval) {
		inputMap := collectRanges(client, modbus.INPUT_REGISTER, inputRanges, runtimeMaxBlockSize)
		holdingMap := collectRanges(client, modbus.HOLDING_REGISTER, holdingRanges, runtimeMaxBlockSize)

			// Decode input registers
			decoded := DecodeInputMap(inputMap)

			// Merge external sensor values from holding registers (per spec)
			for i := 0; i < ExtSensInstances; i++ {
				base := AddrExtSensBase + uint16(i*10)
				decoded.ExtSensPresent[i] = u16(holdingMap, base)
				decoded.ExtSensInvalidate[i] = u16(holdingMap, base+1)
				decoded.ExtSensTemp[i] = i16f(holdingMap, base+2, 0.1)
				decoded.ExtSensRH[i] = u16f(holdingMap, base+3, 1.0)
				decoded.ExtSensCo2[i] = u16(holdingMap, base+4)
				decoded.ExtSensTFloor[i] = i16f(holdingMap, base+5, 0.1)
				log.Printf("Merged ExtSens[%d] from holding: present=%d temp=%.1f RH=%.1f CO2=%d floor=%.1f", i+1, decoded.ExtSensPresent[i], decoded.ExtSensTemp[i], decoded.ExtSensRH[i], decoded.ExtSensCo2[i], decoded.ExtSensTFloor[i])
			}

			// Also merge external button state so Prometheus and other consumers can see it
			for i := 0; i < HoldingExtBtnInstances; i++ {
				base := AddrHoldingExtBtnBase + uint16(i*10)
				decoded.ExtBtnPresent[i] = u16(holdingMap, base)
				decoded.ExtBtnMode[i] = u16(holdingMap, base+1)
				decoded.ExtBtnTm[i] = u16(holdingMap, base+2)
				decoded.ExtBtnActive[i] = u16(holdingMap, base+3)
				log.Printf("Merged ExtBtn[%d] from holding: present=%d mode=%d tm=%d active=%d", i+1, decoded.ExtBtnPresent[i], decoded.ExtBtnMode[i], decoded.ExtBtnTm[i], decoded.ExtBtnActive[i])
			}

			// Update Prometheus metrics
			UpdatePrometheus(decoded)

		log.Printf("Poll complete: inputs=%d, holdings=%d", len(inputMap), len(holdingMap))
	}
}

// collectRanges reads a set of ranges and returns a map[address]value
func collectRanges(client *modbus.ModbusClient, regType modbus.RegType, ranges [][]uint16, maxBlockSize uint16) map[uint16]uint16 {
	out := map[uint16]uint16{}

	for _, r := range ranges {
		start, end := r[0], r[1]
		totalToRead := (end - start) + 1

		for i := uint16(0); i < totalToRead; i += maxBlockSize {
			batchStart := start + i
			batchQuantity := maxBlockSize

			if i+batchQuantity > totalToRead {
				batchQuantity = totalToRead - i
			}

			regs, err := client.ReadRegisters(batchStart, batchQuantity, regType)
			if err != nil {
				log.Printf("ReadRegisters error for %d-%d: %v", batchStart, batchStart+batchQuantity-1, err)

					// Attempt to recover from network errors by reopening the connection once and retrying
					_ = client.Close()
					time.Sleep(500 * time.Millisecond)
					if err2 := client.Open(); err2 != nil {
						log.Printf("Re-open failed: %v", err2)
						continue
					}

					// Retry the read once
					regs, err = client.ReadRegisters(batchStart, batchQuantity, regType)
					if err != nil {
						log.Printf("ReadRegisters retry failed for %d-%d: %v", batchStart, batchStart+batchQuantity-1, err)
						continue
					}
				}

				for idx, val := range regs {
				addr := batchStart + uint16(idx)
				out[addr] = val
			}
		}
	}

	return out
}

func validateRanges(name string, ranges [][]uint16, maxAddr uint16) {
	for idx, r := range ranges {
		if len(r) != 2 {
			log.Fatalf("%s range %d must have exactly 2 values", name, idx)
		}
		start, end := r[0], r[1]
		if start > end {
			log.Fatalf("%s range %d has start > end (%d > %d)", name, idx, start, end)
		}
		if end > maxAddr {
			log.Fatalf("%s range %d exceeds max address %d (end=%d)", name, idx, maxAddr, end)
		}
	}
}


// writeRegisters writes holding registers to the device
// NOTE: This implementation only performs single-register writes. It will
// never write registers in batches — each address is written individually.
func writeRegisters(client *modbus.ModbusClient, registerMap map[uint16]uint16) error {
	if len(registerMap) == 0 {
		return nil
	}

	// Write every register individually (no batch writes)
	for addr, val := range registerMap {
		log.Printf("Writing register %d = 0x%04X", addr, val)
		if err := client.WriteRegister(addr, val); err != nil {
			return fmt.Errorf("write register %d: %w", addr, err)
		}
	}

	return nil
}

// handleIndex redirects to /edit
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/edit", http.StatusFound)
}

// handleEditPage serves the HTML editing interface
func handleEditPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := `<!DOCTYPE html>
<html>
<head>
	<title>Jablotron Futura - Register Editor</title>
	<style>
		* { font-family: Arial, sans-serif; }
		body { margin: 20px; background: #f5f5f5; }
		h1 { color: #333; }
		.container { max-width: 1200px; margin: 0 auto; background: white; padding: 20px; border-radius: 8px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
		.section { margin: 20px 0; padding: 15px; border-left: 4px solid #007bff; background: #f9f9f9; }
		.section h2 { margin-top: 0; color: #007bff; }
		.form-group { margin: 12px 0; }
		label { display: block; font-weight: bold; margin-bottom: 4px; color: #333; }
		input[type="text"], input[type="number"], select { padding: 8px; width: 200px; border: 1px solid #ddd; border-radius: 4px; }
		input[type="checkbox"] { margin-right: 8px; }
		button { padding: 10px 20px; background: #28a745; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 16px; }
		button:hover { background: #218838; }
		.status { margin-top: 20px; padding: 10px; border-radius: 4px; }
		.status.success { background: #d4edda; color: #155724; border: 1px solid #c3e6cb; }
		.status.error { background: #f8d7da; color: #721c24; border: 1px solid #f5c6cb; }
		.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 20px; }			.alfa-card { padding: 8px; border: 1px solid #eee; border-radius: 6px; margin: 6px 0; background: #fff; }	</style>
</head>
<body>
	<div class="container">
		<h1>🏠 Jablotron Futura - Register Editor</h1>
		<p>Edit holding registers and apply changes to the device.</p>

		<form id="editForm">
			<div class="grid">
				<!-- Ventilation & Functions -->
				<div class="section">
					<h2>Ventilation & Functions</h2>
					<div class="form-group">
						<label for="FuncVentilation">Ventilation Level (0-6):</label>
						<input type="number" id="FuncVentilation" name="FuncVentilation" min="0" max="6">
					</div>
					<div class="form-group">
						<label for="FuncBoostTm">Boost Timer (seconds):</label>
						<input type="number" id="FuncBoostTm" name="FuncBoostTm" min="0" max="7200">
					</div>
					<div class="form-group">
						<label for="FuncCirculationTm">Circulation Timer (seconds):</label>
						<input type="number" id="FuncCirculationTm" name="FuncCirculationTm" min="0" max="7200">
					</div>
					<div class="form-group">
						<label for="FuncPartyTm">Party Timer (seconds):</label>
						<input type="number" id="FuncPartyTm" name="FuncPartyTm" min="0" max="28800">
					</div>
					<div class="form-group">
						<label for="FuncNightTm">Night Timer (seconds):</label>
						<input type="number" id="FuncNightTm" name="FuncNightTm" min="0" max="7200">
					</div>
					<div class="form-group">
						<label for="FuncOverpressureTm">Overpressure Timer (seconds):</label>
						<input type="number" id="FuncOverpressureTm" name="FuncOverpressureTm" min="0" max="7200">
					</div>
				</div>

				<!-- Configuration -->
				<div class="section">
					<h2>Configuration</h2>
					<div class="form-group">
						<label for="CfgTempSet">Target Temperature (°C):</label>
						<input type="number" id="CfgTempSet" name="CfgTempSet" step="0.1" min="-20" max="50">
					</div>
					<div class="form-group">
						<label for="CfgHumiSet">Target Humidity (%):</label>
						<input type="number" id="CfgHumiSet" name="CfgHumiSet" step="0.1" min="0" max="100">
					</div>
					<div class="form-group">
						<label><input type="checkbox" id="CfgBypassEnable" name="CfgBypassEnable"> Enable Bypass</label>
					</div>
					<div class="form-group">
						<label><input type="checkbox" id="CfgHeatingEnable" name="CfgHeatingEnable"> Enable Heating</label>
					</div>
					<div class="form-group">
						<label><input type="checkbox" id="CfgCoolingEnable" name="CfgCoolingEnable"> Enable Cooling</label>
					</div>
					<div class="form-group">
						<label><input type="checkbox" id="CfgComfortEnable" name="CfgComfortEnable"> Enable Comfort Control</label>
					</div>
					<div class="form-group">
						<label><input type="checkbox" id="FuncTimeProg" name="FuncTimeProg"> Enable Time Program</label>
					</div>
					<div class="form-group">
						<label><input type="checkbox" id="FuncAntiradon" name="FuncAntiradon"> Enable Radon Protection</label>
					</div>
				</div>

				<!-- HVAC Settings -->
				<div class="section">
					<h2>HVAC Settings</h2>
					<div class="form-group">
						<label for="VzvCBPriorityControl">Coolbreeze Priority (0=Temp, 1=CO2):</label>
						<select id="VzvCBPriorityControl" name="VzvCBPriorityControl">
							<option value="0">Temperature</option>
							<option value="1">CO2</option>
						</select>
					</div>
					<div class="form-group">
						<label><input type="checkbox" id="VzvKitchenhoodNormallyOpen" name="VzvKitchenhoodNormallyOpen"> Hood Normally Open</label>
					</div>
					<div class="form-group">
						<label for="VzvBoostVolumePerRun">Boost Volume (m³/h):</label>
						<input type="number" id="VzvBoostVolumePerRun" name="VzvBoostVolumePerRun" min="50" max="150">
					</div>
					<div class="form-group">
						<label for="VzvKitchenhoodNormallyOpenVolume">Hood Volume (m³/h):</label>
						<input type="number" id="VzvKitchenhoodNormallyOpenVolume" name="VzvKitchenhoodNormallyOpenVolume" min="50" max="150">
					</div>
				</div>

				<!-- Main Unit -->
				<div class="section">
					<h2>Main Unit</h2>
					<div id="mainUnitContainer">Loading main unit data...</div>
				</div>

				<!-- External Sensors -->
				<div class="section">
					<h2>External Sensors</h2>
					<div id="extSensContainer">Loading external sensors...</div>
				</div>

				<!-- ALFA Controllers -->
				<div class="section">
					<h2>ALFA Controllers</h2>
					<div id="alfaContainer">Loading ALFA data...</div>
				</div>
			</div>

			<div style="margin-top: 30px;">
				<button type="submit">Apply Changes</button>
			</div>
		</form>

		<div id="status"></div>
	</div>

	<script>
		// Load current values
		async function loadValues() {
			try {
				const res = await fetch('/api/read-holding');
				const data = await res.json();
				// cache holding data for later use when dynamic inputs are created
				window.holdingData = data;
				// Map data to form fields
				document.getElementById('FuncVentilation').value = data.FuncVentilation || '';
				document.getElementById('FuncBoostTm').value = data.FuncBoostTm || '';
				document.getElementById('FuncCirculationTm').value = data.FuncCirculationTm || '';
				document.getElementById('FuncPartyTm').value = data.FuncPartyTm || '';
				document.getElementById('FuncNightTm').value = data.FuncNightTm || '';
				document.getElementById('FuncOverpressureTm').value = data.FuncOverpressureTm || '';
				document.getElementById('CfgTempSet').value = data.CfgTempSet || '';
				document.getElementById('CfgHumiSet').value = data.CfgHumiSet || '';
				document.getElementById('CfgBypassEnable').checked = data.CfgBypassEnable === 1;
				document.getElementById('CfgHeatingEnable').checked = data.CfgHeatingEnable === 1;
				document.getElementById('CfgCoolingEnable').checked = data.CfgCoolingEnable === 1;
				document.getElementById('CfgComfortEnable').checked = data.CfgComfortEnable === 1;
				document.getElementById('FuncTimeProg').checked = data.FuncTimeProg === 1;
				document.getElementById('FuncAntiradon').checked = data.FuncAntiradon === 1;
				document.getElementById('VzvCBPriorityControl').value = data.VzvCBPriorityControl || '0';
				document.getElementById('VzvKitchenhoodNormallyOpen').checked = data.VzvKitchenhoodNormallyOpen === 1;
				document.getElementById('VzvBoostVolumePerRun').value = data.VzvBoostVolumePerRun || '';
				document.getElementById('VzvKitchenhoodNormallyOpenVolume').value = data.VzvKitchenhoodNormallyOpenVolume || '';
				// populate ext sensor correction inputs if already present
				for (let i = 1; i <= 8; i++) {
					const el = document.getElementById('ExtSensTempCorr' + i);
					if (el && data.ExtSensTempCorr && data.ExtSensTempCorr.length >= i) {
						el.value = data.ExtSensTempCorr[i-1];
					}
				}
				
				showStatus('Values loaded successfully', 'success');
				// After loading initial values, attach auto-save listeners
				attachAutoSaveListeners();
			} catch (err) {
				showStatus('Error loading values: ' + err.message, 'error');
			}
		}

		// Submit form (kept for bulk apply when desired)
		document.getElementById('editForm').addEventListener('submit', async (e) => {
			e.preventDefault();
			// Bulk apply still supported
			const formData = {
				FuncVentilation: parseInt(document.getElementById('FuncVentilation').value) || 0,
				FuncBoostTm: parseInt(document.getElementById('FuncBoostTm').value) || 0,
				FuncCirculationTm: parseInt(document.getElementById('FuncCirculationTm').value) || 0,
				FuncPartyTm: parseInt(document.getElementById('FuncPartyTm').value) || 0,
				FuncNightTm: parseInt(document.getElementById('FuncNightTm').value) || 0,
				FuncOverpressureTm: parseInt(document.getElementById('FuncOverpressureTm').value) || 0,
				CfgTempSet: parseFloat(document.getElementById('CfgTempSet').value) || 0,
				CfgHumiSet: parseFloat(document.getElementById('CfgHumiSet').value) || 0,
				CfgBypassEnable: document.getElementById('CfgBypassEnable').checked ? 1 : 0,
				CfgHeatingEnable: document.getElementById('CfgHeatingEnable').checked ? 1 : 0,
				CfgCoolingEnable: document.getElementById('CfgCoolingEnable').checked ? 1 : 0,
				CfgComfortEnable: document.getElementById('CfgComfortEnable').checked ? 1 : 0,
				FuncTimeProg: document.getElementById('FuncTimeProg').checked ? 1 : 0,
				FuncAntiradon: document.getElementById('FuncAntiradon').checked ? 1 : 0,
				VzvCBPriorityControl: parseInt(document.getElementById('VzvCBPriorityControl').value) || 0,
				VzvKitchenhoodNormallyOpen: document.getElementById('VzvKitchenhoodNormallyOpen').checked ? 1 : 0,
				VzvBoostVolumePerRun: parseInt(document.getElementById('VzvBoostVolumePerRun').value) || 0,
				VzvKitchenhoodNormallyOpenVolume: parseInt(document.getElementById('VzvKitchenhoodNormallyOpenVolume').value) || 0,
			};
			
			try {
				const res = await fetch('/api/write-holding', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(formData)
				});
				const result = await res.json();
				if (result.success) {
					showStatus('✅ Changes applied successfully!', 'success');
				} else {
					showStatus('❌ Error: ' + (result.error || 'Unknown error'), 'error');
				}
			} catch (err) {
				showStatus('❌ Error writing values: ' + err.message, 'error');
			}
		});

		// helper: post a single field to the backend
		async function postSingleField(name, value) {
			try {
				const body = {};
				body[name] = value;
				const res = await fetch('/api/write-holding', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(body)
				});
				const result = await res.json();
				if (result.success) {
					showStatus('Saved ' + name, 'success');
				} else {
					showStatus('Error saving ' + name + ': ' + (result.error || 'unknown'), 'error');
				}
			} catch (err) {
				showStatus('Error saving ' + name + ': ' + err.message, 'error');
			}
		}

		// debounce helper
		function debounce(fn, wait) {
			let t;
			return function() {
				const args = arguments;
				const ctx = this;
				clearTimeout(t);
				t = setTimeout(function() { fn.apply(ctx, args); }, wait);
			};
		}

		// attach listeners to fields for immediate single-field writes
		function attachAutoSaveListeners() {
			const elems = document.querySelectorAll('#editForm input, #editForm select');
			elems.forEach(el => {
				const id = el.id;
				if (!id) return;

				const send = () => {
					if (el.type === 'checkbox') {
						postSingleField(id, el.checked ? 1 : 0);
						return;
					}
					if (el.tagName === 'SELECT') {
						postSingleField(id, parseInt(el.value) || 0);
						return;
					}
					// Number inputs: support float or int depending on step
					if (el.type === 'number') {
						if (!el.value) return; // don't post empty
						if (el.step && el.step.indexOf('.') !== -1) {
							postSingleField(id, parseFloat(el.value) || 0);
						} else {
							postSingleField(id, parseInt(el.value) || 0);
						}
						return;
					}
					// fallback: send string value
					postSingleField(id, el.value);
				};

				if (el.type === 'number') {
					el.addEventListener('input', debounce(send, 400));
					el.addEventListener('change', send);
				} else {
					el.addEventListener('change', send);
				}
			});
		}

		function showStatus(msg, type) {
			const status = document.getElementById('status');
			status.className = 'status ' + type;
			status.textContent = msg;
			status.style.display = 'block';
			// auto-hide after 3s
			setTimeout(() => { status.style.display = 'none'; }, 3000);
		}

		// Load on page load
		loadValues();
		// Load ALFA values and refresh periodically
		async function loadAlfas() {
			try {
				const res = await fetch('/api/read-input');
				const data = await res.json();				// Main unit summary
				const main = document.getElementById('mainUnitContainer');
				let mainOut = '';
				mainOut += '<div class="section"><strong>Main Unit</strong><br>';
				mainOut += 'Device ID: ' + (data.FactDeviceID !== undefined ? data.FactDeviceID : '—') + '<br>'; 
				mainOut += 'Serial: ' + (data.FactSerialNum !== undefined ? data.FactSerialNum : '—') + '<br>';
				mainOut += 'Ambient: ' + (data.TempAmbient !== undefined ? data.TempAmbient.toFixed(1) + '°C' : '—') + '<br>';
				mainOut += 'Fresh: ' + (data.TempFresh !== undefined ? data.TempFresh.toFixed(1) + '°C' : '—') + '<br>';
				mainOut += 'Indoor: ' + (data.TempIndoor !== undefined ? data.TempIndoor.toFixed(1) + '°C' : '—') + '<br>';
				mainOut += 'Waste: ' + (data.TempWaste !== undefined ? data.TempWaste.toFixed(1) + '°C' : '—') + '<br>';
				mainOut += 'Humi Ambient: ' + (data.HumiAmbient !== undefined ? data.HumiAmbient.toFixed(1) + '%' : '—') + '<br>';
				mainOut += 'Humi Fresh: ' + (data.HumiFresh !== undefined ? data.HumiFresh.toFixed(1) + '%' : '—') + '<br>';
				mainOut += 'Humi Indoor: ' + (data.HumiIndoor !== undefined ? data.HumiIndoor.toFixed(1) + '%' : '—') + '<br>';
				mainOut += 'Humi Waste: ' + (data.HumiWaste !== undefined ? data.HumiWaste.toFixed(1) + '%' : '—') + '<br>';
				mainOut += 'Filter Wear: ' + (data.FilterWear !== undefined ? data.FilterWear + '%' : '—') + '<br>';
				mainOut += 'Air Flow: ' + (data.AirFlow !== undefined ? data.AirFlow : '—') + '<br>';
				mainOut += 'Power: ' + (data.PowerConsumption !== undefined ? data.PowerConsumption : '—') + '<br>';
				mainOut += 'Fan RPM Supply: ' + (data.FanRPMSupply !== undefined ? data.FanRPMSupply : '—') + '<br>';
				mainOut += 'Fan RPM Exhaust: ' + (data.FanRPMExhaust !== undefined ? data.FanRPMExhaust : '—') + '<br>';
				mainOut += '</div>';
				main.innerHTML = mainOut;				// External sensors: always show 8 slots
				const extContainer = document.getElementById('extSensContainer');
				if (extContainer) {
					let extOut = '';
					for (let i = 0; i < 8; i++) {
						const idx = i + 1;
						const present = data.ExtSensPresent && data.ExtSensPresent[i];
						const invalidate = data.ExtSensInvalidate && data.ExtSensInvalidate[i];
						extOut += '<div class="section'>
						extOut += '<strong>Ext Sens ' + idx + (present ? '' : ' (not present)') + '</strong><br>';
						extOut += 'Present: <label><input type="checkbox" id="ExtSensPresent' + idx + '"' + (present ? ' checked' : '') + '> </label><br>';
					// Invalidate: only show documented bits with names
					extOut += 'Invalidate:';
					const invalidateBits = [
						{bit:0, name: 'Neplatná hodnota teploty externího čidla'},
						{bit:1, name: 'Neplatná hodnota vlhkosti externího čidla'},
						{bit:2, name: 'Neplatná hodnota CO2 externího čidla'},
						{bit:3, name: 'Neplatná hodnota teploty podlahy externího čidla'},
					];
					for (let ib = 0; ib < invalidateBits.length; ib++) {
						const b = invalidateBits[ib].bit;
						const label = invalidateBits[ib].name;
						const checked = (invalidate & (1 << b)) ? ' checked' : '';
						extOut += ' <label title="' + label + '"><input type="checkbox" class="ExtSensInvalidate' + idx + '_bit" data-sensor="' + idx + '" data-bit="' + b + '"' + checked + '> ' + label + '</label>';
						}
						extOut += '<br>';
						// Editable live values
						extOut += 'Temp: <input type="number" id="ExtSensTemp' + idx + '" step="0.1" min="-50" max="100" value="' + (data.ExtSensTemp && data.ExtSensTemp[i] !== undefined ? data.ExtSensTemp[i].toFixed(1) : '') + '"> °C<br>';
						extOut += 'RH: <input type="number" id="ExtSensRH' + idx + '" step="1" min="0" max="100" value="' + (data.ExtSensRH && data.ExtSensRH[i] !== undefined ? data.ExtSensRH[i] : '') + '"> %<br>';
						extOut += 'CO2: <input type="number" id="ExtSensCo2' + idx + '" step="1" min="0" max="10000" value="' + (data.ExtSensCo2 && data.ExtSensCo2[i] !== undefined ? data.ExtSensCo2[i] : '') + '"> ppm<br>';
						extOut += 'Floor: <input type="number" id="ExtSensTFloor' + idx + '" step="0.1" min="-50" max="100" value="' + (data.ExtSensTFloor && data.ExtSensTFloor[i] !== undefined ? data.ExtSensTFloor[i].toFixed(1) : '') + '"> °C<br>';
						extOut += 'Correction: <input type="number" id="ExtSensTempCorr' + idx + '" step="0.1" min="-50" max="50">';
						extOut += '</div>';
					}
					extContainer.innerHTML = extOut;
					// populate correction inputs from cached holding data
					if (window.holdingData && window.holdingData.ExtSensTempCorr) {
						for (let i = 1; i <= 8; i++) {
							const el = document.getElementById('ExtSensTempCorr' + i);
							if (el && window.holdingData.ExtSensTempCorr.length >= i) el.value = window.holdingData.ExtSensTempCorr[i-1];
						}
					}
					// attach invalidate bit listeners (recomputed mask write)
					for (let si = 1; si <= 8; si++) {
						const bits = document.querySelectorAll('.ExtSensInvalidate' + si + '_bit');
						if (!bits) continue;
						bits.forEach(cb => cb.addEventListener('change', () => {
							let mask = 0;
							bits.forEach(b => {
								if (b.checked) mask |= (1 << parseInt(b.dataset.bit));
							});
							postSingleField('ExtSensInvalidate' + si, mask);
						}));
					}
					// Ensure listeners for newly created inputs
					attachAutoSaveListeners();
				}
				const container = document.getElementById('alfaContainer');
				let out = '';
				if (!data.AlfaMBAddress) {
					container.textContent = 'No ALFA data available';
					return;
				}
				for (let i = 0; i < data.AlfaMBAddress.length; i++) {
					if (!data.AlfaMBAddress[i]) continue; // not present
					const idx = i + 1;
				out += '<div class="section"><strong>ALFA ' + idx + '</strong><br>';
				out += 'Address: ' + data.AlfaMBAddress[i] + '<br>';
				out += 'Temp: ' + (data.AlfaTemp && data.AlfaTemp[i] !== undefined ? data.AlfaTemp[i].toFixed(1) + '°C' : '—') + '<br>';
				out += 'Humi: ' + (data.AlfaHumi && data.AlfaHumi[i] !== undefined ? data.AlfaHumi[i].toFixed(1) + '%' : '—') + '<br>';
				out += 'NTC Temp: ' + (data.AlfaNTCTemp && data.AlfaNTCTemp[i] !== undefined ? data.AlfaNTCTemp[i].toFixed(1) + '°C' : '—') + '<br>';
				out += '</div>';
				}
				container.innerHTML = out || 'No ALFA controllers present';
					// populate ext sensor correction inputs from cached holding data (if any)
					if (window.holdingData && window.holdingData.ExtSensTempCorr) {
						for (let i = 1; i <= 8; i++) {
							const el = document.getElementById('ExtSensTempCorr' + i);
							if (el && window.holdingData.ExtSensTempCorr.length >= i) el.value = window.holdingData.ExtSensTempCorr[i-1];
						}
					}
					// Re-attach listeners to newly-created inputs
					attachAutoSaveListeners();
			} catch (err) {
				document.getElementById('alfaContainer').textContent = 'Error loading ALFA: ' + err.message;
			}
		}
		// initial load and periodic refresh every 5s
		loadAlfas();
		setInterval(loadAlfas, 5000);
	</script>
</body>
</html>`
	_ = html
	if editTmpl != nil {
		if err := editTmpl.Execute(w, nil); err != nil {
			log.Printf("render edit template: %v", err)
			http.Error(w, "internal render error", http.StatusInternalServerError)
		}
	} else {
		w.Write([]byte(editHTML))
	}
}

// handleReadHolding returns current holding register values as JSON
func handleReadHolding(client *modbus.ModbusClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		
		holdingMap := collectRanges(client, modbus.HOLDING_REGISTER, holdingRanges, runtimeMaxBlockSize)
		holding := DecodeHoldingMap(holdingMap)
		
		// Return as JSON
		if err := json.NewEncoder(w).Encode(holding); err != nil {
			log.Printf("encode holding json: %v", err)
			http.Error(w, "internal encode error", http.StatusInternalServerError)
		}
	}
}

// handleWriteHolding processes POST requests to write holding registers
func handleWriteHolding(client *modbus.ModbusClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		
		if r.Method != http.MethodPost {
			fmt.Fprintf(w, `{"success":false,"error":"POST required"}`)
			return
		}

		var data map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			fmt.Fprintf(w, `{"success":false,"error":"Invalid JSON"}`)
			return
		}

		// If a single field is provided write only that register
		if len(data) == 1 {
			for k, v := range data {
				// v is typically float64 from json decoding
				val, ok := v.(float64)
				if !ok {
					fmt.Fprintf(w, `{"success":false,"error":"invalid value type"}`)
					return
				}
				log.Printf("Single write requested: %s = %v", k, val)
				if err := WriteSingleRegister(client, k, val); err != nil {
					log.Printf("Single write error: %v", err)
					fmt.Fprintf(w, `{"success":false,"error":"%s"}` , err.Error())
					return
				}
				log.Printf("Single write success: %s = %v", k, val)
				fmt.Fprintf(w, `{"success":true,"message":"%s updated"}` , k)
				return
			}
		}

		// Otherwise do a full holding update (writes potentially multiple registers)
		// Read current holding registers
		holdingMap := collectRanges(client, modbus.HOLDING_REGISTER, holdingRanges, runtimeMaxBlockSize)
		holding := DecodeHoldingMap(holdingMap)

		// Update with provided values
		if v, ok := data["FuncVentilation"]; ok {
			holding.FuncVentilation = uint16(v.(float64))
		}
		if v, ok := data["FuncBoostTm"]; ok {
			holding.FuncBoostTm = uint16(v.(float64))
		}
		if v, ok := data["FuncCirculationTm"]; ok {
			holding.FuncCirculationTm = uint16(v.(float64))
		}
		if v, ok := data["FuncPartyTm"]; ok {
			holding.FuncPartyTm = uint16(v.(float64))
		}
		if v, ok := data["FuncNightTm"]; ok {
			holding.FuncNightTm = uint16(v.(float64))
		}
		if v, ok := data["FuncOverpressureTm"]; ok {
			holding.FuncOverpressureTm = uint16(v.(float64))
		}
		if v, ok := data["CfgTempSet"]; ok {
			holding.CfgTempSet = v.(float64)
		}
		if v, ok := data["CfgHumiSet"]; ok {
			holding.CfgHumiSet = v.(float64)
		}
		if v, ok := data["CfgBypassEnable"]; ok {
			holding.CfgBypassEnable = uint16(v.(float64))
		}
		if v, ok := data["CfgHeatingEnable"]; ok {
			holding.CfgHeatingEnable = uint16(v.(float64))
		}
		if v, ok := data["CfgCoolingEnable"]; ok {
			holding.CfgCoolingEnable = uint16(v.(float64))
		}
		if v, ok := data["CfgComfortEnable"]; ok {
			holding.CfgComfortEnable = uint16(v.(float64))
		}
		if v, ok := data["FuncTimeProg"]; ok {
			holding.FuncTimeProg = uint16(v.(float64))
		}
		if v, ok := data["FuncAntiradon"]; ok {
			holding.FuncAntiradon = uint16(v.(float64))
		}
		if v, ok := data["VzvCBPriorityControl"]; ok {
			holding.VzvCBPriorityControl = uint16(v.(float64))
		}
		if v, ok := data["VzvKitchenhoodNormallyOpen"]; ok {
			holding.VzvKitchenhoodNormallyOpen = uint16(v.(float64))
		}
		if v, ok := data["VzvBoostVolumePerRun"]; ok {
			holding.VzvBoostVolumePerRun = uint16(v.(float64))
		}
		if v, ok := data["VzvKitchenhoodNormallyOpenVolume"]; ok {
			holding.VzvKitchenhoodNormallyOpenVolume = uint16(v.(float64))
		}

		// Encode and write
		encoded := EncodeHoldingRegs(holding)
		if err := writeRegisters(client, encoded); err != nil {
			log.Printf("Write error: %v", err)
			fmt.Fprintf(w, `{"success":false,"error":"%s"}`, err.Error())
			return
		}
		log.Printf("Bulk write completed: %d registers written", len(encoded))

		fmt.Fprintf(w, `{"success":true,"message":"Registers updated successfully"}`)
	}
}

// handleReadInput returns current input register values as JSON
func handleReadInput(client *modbus.ModbusClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		inputMap := collectRanges(client, modbus.INPUT_REGISTER, inputRanges, runtimeMaxBlockSize)
	input := DecodeInputMap(inputMap)

	// Also read holding registers and prefer external sensor values from holdings
	holdingMap := collectRanges(client, modbus.HOLDING_REGISTER, holdingRanges, runtimeMaxBlockSize)
	// Override ext sensor fields with holding values (if present)
	for i := 0; i < ExtSensInstances; i++ {
		base := AddrExtSensBase + uint16(i*10)
		input.ExtSensPresent[i] = u16(holdingMap, base)
		input.ExtSensInvalidate[i] = u16(holdingMap, base+1)
		input.ExtSensTemp[i] = i16f(holdingMap, base+2, 0.1)
		input.ExtSensRH[i] = u16f(holdingMap, base+3, 1.0)
		input.ExtSensCo2[i] = u16(holdingMap, base+4)
		input.ExtSensTFloor[i] = i16f(holdingMap, base+5, 0.1)
		// Debug log showing raw holding-derived ext sensor values
		log.Printf("ExtSens[%d] holding base=%d present=%d invalidate=%d temp=%.1f RH=%.1f CO2=%d floor=%.1f",
			i+1, base, input.ExtSensPresent[i], input.ExtSensInvalidate[i], input.ExtSensTemp[i], input.ExtSensRH[i], input.ExtSensCo2[i], input.ExtSensTFloor[i])
	}

		// Merge external button values from holdings so read-input includes them too
		for i := 0; i < HoldingExtBtnInstances; i++ {
			base := AddrHoldingExtBtnBase + uint16(i*10)
			input.ExtBtnPresent[i] = u16(holdingMap, base)
			input.ExtBtnMode[i] = u16(holdingMap, base+1)
			input.ExtBtnTm[i] = u16(holdingMap, base+2)
			input.ExtBtnActive[i] = u16(holdingMap, base+3)
			log.Printf("ExtBtn[%d] holding base=%d present=%d mode=%d tm=%d active=%d",
				i+1, base, input.ExtBtnPresent[i], input.ExtBtnMode[i], input.ExtBtnTm[i], input.ExtBtnActive[i])
		}

		if err := json.NewEncoder(w).Encode(input); err != nil {
			log.Printf("encode input json: %v", err)
			http.Error(w, "internal encode error", http.StatusInternalServerError)
		}
	}
}
