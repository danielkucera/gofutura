package main

import (
	"fmt"
	"log"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/simonvetter/modbus"
)

// DamperType indicates whether it's a supply or exhaust damper
type DamperType int

const (
	DamperTypeSupply DamperType = iota
	DamperTypeExhaust
)

const (
	damperPositionRegister  = uint16(102)
	greenLEDModeRegister    = uint16(108)
	redLEDModeRegister      = uint16(109)
	redLEDOnTimeRegister    = uint16(111)
	redLEDOffTimeRegister   = uint16(112)
	greenLEDOnTimeRegister  = uint16(113)
	greenLEDOffTimeRegister = uint16(114)

	LEDBlinkMode = uint16(65534)
	LEDOnMode    = uint16(65535)
	LEDOffMode   = uint16(0)

	redLEDPeriod = uint16(500)
)

// Damper represents a single damper device on the modbus bus
type Damper struct {
	SlaveID    uint8      // Modbus slave ID
	Type       DamperType // Supply or Exhaust
	Zone       uint8      // Zone 1-8
	Index      uint8      // Damper index (0-2 for up to 3 dampers per zone)
	Position   uint16     // Current position (0-100 or similar)
	LastUpdate int64      // Unix timestamp of last update
}

// DamperBus manages all dampers on a modbus bus
type DamperBus struct {
	client  *modbus.ModbusClient
	dampers map[uint8]*Damper // slaveID -> Damper
	mu      sync.RWMutex
	metrics *DamperMetrics
}

// DamperMetrics holds prometheus metrics for dampers
type DamperMetrics struct {
	position *prometheus.GaugeVec // damper_position{type,zone,index}
}

// NewDamperBus creates a new damper bus manager
func NewDamperBus(client *modbus.ModbusClient) *DamperBus {
	db := &DamperBus{
		client:  client,
		dampers: make(map[uint8]*Damper),
		metrics: &DamperMetrics{
			position: prometheus.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "damper_position",
					Help: "Damper position (0-100 or device-specific range)",
				},
				[]string{"type", "zone", "index"},
			),
		},
	}

	// Register metrics
	prometheus.MustRegister(db.metrics.position)

	return db
}

// ScanBus discovers all dampers on the modbus bus
// Supply dampers: slave_id = 64 + (index-1)*8 + (zone-1)
// Exhaust dampers: slave_id = 96 + (index-1)*8 + (zone-1)
// Zones: 1-8, Indices: 1-3 (up to 3 dampers per zone)
func (db *DamperBus) ScanBus() error {
	log.Println("Scanning for dampers on modbus bus...")

	db.mu.Lock()
	defer db.mu.Unlock()

	// Clear existing dampers
	db.dampers = make(map[uint8]*Damper)

	damperCount := 0

	// Scan supply dampers (64-87: 3 indices × 8 zones)
	for index := uint8(1); index <= 3; index++ {
		for zone := uint8(1); zone <= 8; zone++ {
			slaveID := uint8(64 + (index-1)*8 + (zone - 1))
			if db.pingDevice(slaveID) {
				damper := &Damper{
					SlaveID: slaveID,
					Type:    DamperTypeSupply,
					Zone:    zone,
					Index:   index,
				}
				db.dampers[slaveID] = damper
				log.Printf("Found supply damper: slave_id=%d zone=%d index=%d", slaveID, zone, index)
				damperCount++
			}
		}
	}

	// Scan exhaust dampers (96-119: 3 indices × 8 zones)
	for index := uint8(1); index <= 3; index++ {
		for zone := uint8(1); zone <= 8; zone++ {
			slaveID := uint8(96 + (index-1)*8 + (zone - 1))
			if db.pingDevice(slaveID) {
				damper := &Damper{
					SlaveID: slaveID,
					Type:    DamperTypeExhaust,
					Zone:    zone,
					Index:   index,
				}
				db.dampers[slaveID] = damper
				log.Printf("Found exhaust damper: slave_id=%d zone=%d index=%d", slaveID, zone, index)
				damperCount++
			}
		}
	}

	log.Printf("Damper scan complete: found %d dampers", damperCount)
	return nil
}

// pingDevice attempts to read from a device to check if it's alive
func (db *DamperBus) pingDevice(slaveID uint8) bool {
	_ = db.client.SetUnitId(slaveID)

	// Try to read a single holding register (register 102)
	_, err := db.client.ReadRegisters(102, 1, modbus.HOLDING_REGISTER)
	return err == nil
}

// UpdatePositions reads the position register (102) from all dampers
func (db *DamperBus) UpdatePositions() {
	db.mu.RLock()
	dampers := make([]*Damper, 0, len(db.dampers))
	for _, d := range db.dampers {
		dampers = append(dampers, d)
	}
	db.mu.RUnlock()

	for _, damper := range dampers {
		position, err := db.readPosition(damper.SlaveID)
		if err != nil {
			log.Printf("Failed to read position from slave %d: %v", damper.SlaveID, err)
			continue
		}

		db.mu.Lock()
		if d, exists := db.dampers[damper.SlaveID]; exists {
			d.Position = position
		}
		db.mu.Unlock()

		// Update metrics
		typeStr := "supply"
		if damper.Type == DamperTypeExhaust {
			typeStr = "exhaust"
		}
		db.metrics.position.WithLabelValues(
			typeStr,
			fmt.Sprintf("%d", damper.Zone),
			fmt.Sprintf("%d", damper.Index),
		).Set(float64(position))
	}
}

// readPosition reads register 102 from a specific damper
func (db *DamperBus) readPosition(slaveID uint8) (uint16, error) {
	_ = db.client.SetUnitId(slaveID)

	regs, err := db.client.ReadRegisters(damperPositionRegister, 1, modbus.HOLDING_REGISTER)
	if err != nil {
		return 0, err
	}

	if len(regs) > 0 {
		return regs[0], nil
	}
	return 0, fmt.Errorf("no registers returned")
}

func writeSingleDamperRegister(client *modbus.ModbusClient, slaveID uint8, address uint16, value uint16) error {
	log.Printf("Damper write: slave=%d reg=%d value=%d (0x%04X)", slaveID, address, value, value)
	if err := client.WriteRegisters(address, []uint16{value}); err != nil {
		return fmt.Errorf("write register %d failed: %w", address, err)
	}
	return nil
}

func redLEDTimesFromPosition(position uint16) (onTime uint16, offTime uint16) {
	if position > 100 {
		position = 100
	}

	offTime = redLEDPeriod - position*(redLEDPeriod/100)
	onTime = redLEDPeriod - offTime

	return onTime, offTime
}

func (db *DamperBus) writeRedLEDFromPosition(slaveID uint8, position uint16) (onTime uint16, offTime uint16, err error) {
	_ = db.client.SetUnitId(slaveID)

	onTime, offTime = redLEDTimesFromPosition(position)
	if err := writeSingleDamperRegister(db.client, slaveID, redLEDModeRegister, LEDBlinkMode); err != nil {
		return 0, 0, fmt.Errorf("write red led mode failed: %w", err)
	}
	if err := writeSingleDamperRegister(db.client, slaveID, redLEDOnTimeRegister, onTime); err != nil {
		return 0, 0, fmt.Errorf("write red led on-time failed: %w", err)
	}
	if err := writeSingleDamperRegister(db.client, slaveID, redLEDOffTimeRegister, offTime); err != nil {
		return 0, 0, fmt.Errorf("write red led off-time failed: %w", err)
	}

	return onTime, offTime, nil
}

func (db *DamperBus) writeGreenLEDOn(slaveID uint8) error {
	_ = db.client.SetUnitId(slaveID)
	if err := writeSingleDamperRegister(db.client, slaveID, greenLEDModeRegister, LEDOnMode); err != nil {
		return fmt.Errorf("write green led mode failed: %w", err)
	}
	return nil
}

// InitializeLEDsFromCurrentPositions reads position from every discovered damper
// and adjusts red LED timing to match that position.
func (db *DamperBus) InitializeLEDsFromCurrentPositions() error {
	db.mu.RLock()
	dampers := make([]*Damper, 0, len(db.dampers))
	for _, d := range db.dampers {
		dampers = append(dampers, d)
	}
	db.mu.RUnlock()

	var lastErr error
	for _, damper := range dampers {
		position, err := db.readPosition(damper.SlaveID)
		if err != nil {
			log.Printf("Startup LED sync: failed to read position from slave %d: %v", damper.SlaveID, err)
			lastErr = err
			continue
		}

		onTime, offTime, err := db.writeRedLEDFromPosition(damper.SlaveID, position)
		if err != nil {
			log.Printf("Startup LED sync: failed to set red LED for slave %d: %v", damper.SlaveID, err)
			lastErr = err
			continue
		}

		if err := db.writeGreenLEDOn(damper.SlaveID); err != nil {
			log.Printf("Startup LED sync: failed to set green LED on for slave %d: %v", damper.SlaveID, err)
			lastErr = err
			continue
		}

		db.mu.Lock()
		if d, exists := db.dampers[damper.SlaveID]; exists {
			d.Position = position
		}
		db.mu.Unlock()

		typeStr := "supply"
		if damper.Type == DamperTypeExhaust {
			typeStr = "exhaust"
		}
		db.metrics.position.WithLabelValues(
			typeStr,
			fmt.Sprintf("%d", damper.Zone),
			fmt.Sprintf("%d", damper.Index),
		).Set(float64(position))

		log.Printf("Startup LED sync: slave %d position=%d red_led_on=%d red_led_off=%d", damper.SlaveID, position, onTime, offTime)
	}

	return lastErr
}

// SetPosition writes the position to register 102 on a specific damper
func (db *DamperBus) SetPosition(slaveID uint8, position uint16) error {
	_ = db.client.SetUnitId(slaveID)

	if err := writeSingleDamperRegister(db.client, slaveID, damperPositionRegister, position); err != nil {
		return fmt.Errorf("write damper position failed: %w", err)
	}

	onTime, offTime, err := db.writeRedLEDFromPosition(slaveID, position)
	if err != nil {
		return err
	}

	db.mu.Lock()
	if d, exists := db.dampers[slaveID]; exists {
		d.Position = position
	}
	db.mu.Unlock()

	log.Printf("Set damper slave %d position=%d red_led_on=%d red_led_off=%d", slaveID, position, onTime, offTime)
	return nil
}

// SetAllSupplyDampers sets all supply dampers to the same position
func (db *DamperBus) SetAllSupplyDampers(position uint16) error {
	db.mu.RLock()
	dampers := make([]*Damper, 0)
	for _, d := range db.dampers {
		if d.Type == DamperTypeSupply {
			dampers = append(dampers, d)
		}
	}
	db.mu.RUnlock()

	var lastErr error
	for _, d := range dampers {
		if err := db.SetPosition(d.SlaveID, position); err != nil {
			log.Printf("Failed to set supply damper %d: %v", d.SlaveID, err)
			lastErr = err
		}
	}
	return lastErr
}

// SetAllExhaustDampers sets all exhaust dampers to the same position
func (db *DamperBus) SetAllExhaustDampers(position uint16) error {
	db.mu.RLock()
	dampers := make([]*Damper, 0)
	for _, d := range db.dampers {
		if d.Type == DamperTypeExhaust {
			dampers = append(dampers, d)
		}
	}
	db.mu.RUnlock()

	var lastErr error
	for _, d := range dampers {
		if err := db.SetPosition(d.SlaveID, position); err != nil {
			log.Printf("Failed to set exhaust damper %d: %v", d.SlaveID, err)
			lastErr = err
		}
	}
	return lastErr
}

func (db *DamperBus) setSelectedDampersByType(dType DamperType, slaveIDs []uint8, position uint16) error {
	db.mu.RLock()
	allowed := make(map[uint8]struct{})
	for _, d := range db.dampers {
		if d.Type == dType {
			allowed[d.SlaveID] = struct{}{}
		}
	}
	db.mu.RUnlock()

	var lastErr error
	for _, id := range slaveIDs {
		if _, ok := allowed[id]; !ok {
			continue
		}
		if err := db.SetPosition(id, position); err != nil {
			log.Printf("Failed to set selected damper %d: %v", id, err)
			lastErr = err
		}
	}
	return lastErr
}

// SetSelectedSupplyDampers sets selected supply dampers to the same position.
func (db *DamperBus) SetSelectedSupplyDampers(slaveIDs []uint8, position uint16) error {
	return db.setSelectedDampersByType(DamperTypeSupply, slaveIDs, position)
}

// SetSelectedExhaustDampers sets selected exhaust dampers to the same position.
func (db *DamperBus) SetSelectedExhaustDampers(slaveIDs []uint8, position uint16) error {
	return db.setSelectedDampersByType(DamperTypeExhaust, slaveIDs, position)
}

// GetDampers returns a copy of all discovered dampers
func (db *DamperBus) GetDampers() map[uint8]*Damper {
	db.mu.RLock()
	defer db.mu.RUnlock()

	result := make(map[uint8]*Damper)
	for k, v := range db.dampers {
		// Make a copy
		damperCopy := *v
		result[k] = &damperCopy
	}
	return result
}
