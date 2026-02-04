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
				//log.Printf("Merged ExtSens[%d] from holding: present=%d temp=%.1f RH=%.1f CO2=%d floor=%.1f", i+1, decoded.ExtSensPresent[i], decoded.ExtSensTemp[i], decoded.ExtSensRH[i], decoded.ExtSensCo2[i], decoded.ExtSensTFloor[i])
			}

			// Also merge external button state so Prometheus and other consumers can see it
			for i := 0; i < HoldingExtBtnInstances; i++ {
				base := AddrHoldingExtBtnBase + uint16(i*10)
				decoded.ExtBtnPresent[i] = u16(holdingMap, base)
				decoded.ExtBtnMode[i] = u16(holdingMap, base+1)
				decoded.ExtBtnTm[i] = u16(holdingMap, base+2)
				decoded.ExtBtnActive[i] = u16(holdingMap, base+3)
				//log.Printf("Merged ExtBtn[%d] from holding: present=%d mode=%d tm=%d active=%d", i+1, decoded.ExtBtnPresent[i], decoded.ExtBtnMode[i], decoded.ExtBtnTm[i], decoded.ExtBtnActive[i])
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
		//log.Printf("ExtSens[%d] holding base=%d present=%d invalidate=%d temp=%.1f RH=%.1f CO2=%d floor=%.1f",
		//	i+1, base, input.ExtSensPresent[i], input.ExtSensInvalidate[i], input.ExtSensTemp[i], input.ExtSensRH[i], input.ExtSensCo2[i], input.ExtSensTFloor[i])
	}

		// Merge external button values from holdings so read-input includes them too
		for i := 0; i < HoldingExtBtnInstances; i++ {
			base := AddrHoldingExtBtnBase + uint16(i*10)
			input.ExtBtnPresent[i] = u16(holdingMap, base)
			input.ExtBtnMode[i] = u16(holdingMap, base+1)
			input.ExtBtnTm[i] = u16(holdingMap, base+2)
			input.ExtBtnActive[i] = u16(holdingMap, base+3)
			//log.Printf("ExtBtn[%d] holding base=%d present=%d mode=%d tm=%d active=%d",
			//	i+1, base, input.ExtBtnPresent[i], input.ExtBtnMode[i], input.ExtBtnTm[i], input.ExtBtnActive[i])
		}

		if err := json.NewEncoder(w).Encode(input); err != nil {
			log.Printf("encode input json: %v", err)
			http.Error(w, "internal encode error", http.StatusInternalServerError)
		}
	}
}
