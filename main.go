package main

import (
	"fmt"
	"log"
	"time"

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
	{310, 315}, // external sensor 1
	{320, 325}, // external sensor 1
	{330, 335}, // external sensor 1
	{340, 345}, // external sensor 1
	{350, 355}, // external sensor 1
	{360, 365}, // external sensor 1
	{370, 375}, // external sensor 1
}

const (
	// Edit these for your setup
	UnitIP       = "192.168.29.22:502"
	SlaveID      = 1
	
	// PARAMETERS TO TEST YOUR LIMITS
	// Standard Modbus limit is 125. Try increasing/decreasing this.
	MaxBlockSize = 125 
	
	InputMaxAddr   = 255
	HoldingMaxAddr = 1024
)

func main() {
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     "tcp://" + UnitIP,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	err = client.Open()
	if err != nil {
		log.Fatalf("Failed to connect: %v. Is another tool open?", err)
	}
	defer client.Close()

	fmt.Println("=== Reading Defined Input Blocks ===")
	for _, r := range inputRanges {
		readRange(client, modbus.INPUT_REGISTER, r[0], r[1])
	}

	fmt.Println("\n=== Reading Defined Holding Blocks ===")
	for _, r := range holdingRanges {
		readRange(client, modbus.HOLDING_REGISTER, r[0], r[1])
	}
}

func readRange(client *modbus.ModbusClient, regType modbus.RegType, start uint16, end uint16) {
	totalToRead := (end - start) + 1

	for i := uint16(0); i < totalToRead; i += MaxBlockSize {
		batchStart := start + i
		batchQuantity := uint16(MaxBlockSize)

		if i+batchQuantity > totalToRead {
			batchQuantity = totalToRead - i
		}

		fmt.Printf("Range [%d-%d] Type %v: ", batchStart, batchStart+batchQuantity-1, regType)

		regs, err := client.ReadRegisters(batchStart, batchQuantity, regType)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			continue
		}
		fmt.Printf("OK (%d registers)\n", len(regs))

		for idx, val := range regs {
			addr := batchStart + uint16(idx)
			fmt.Printf("  Addr %d: %d (0x%04X)\n", addr, val, val)
		}
	}
}
