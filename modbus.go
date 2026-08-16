package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/simonvetter/modbus"
)

type ResilientModbusClient struct {
	client *modbus.ModbusClient
}

func NewResilientModbusClient(cfg *modbus.ClientConfiguration) (*ResilientModbusClient, error) {
	c, err := modbus.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &ResilientModbusClient{client: c}, nil
}

func (c *ResilientModbusClient) Open() error {
	return c.client.Open()
}

func (c *ResilientModbusClient) Close() error {
	return c.client.Close()
}

func (c *ResilientModbusClient) SetUnitId(unitID uint8) error {
	return c.client.SetUnitId(unitID)
}

func (c *ResilientModbusClient) ReadRegisters(address uint16, quantity uint16, regType modbus.RegType) ([]uint16, error) {
	var regs []uint16
	err := c.withReconnect(func() error {
		var readErr error
		regs, readErr = c.client.ReadRegisters(address, quantity, regType)
		return readErr
	})
	if err != nil {
		return nil, err
	}
	return regs, nil
}

func (c *ResilientModbusClient) WriteRegister(address uint16, value uint16) error {
	return c.withReconnect(func() error {
		return c.client.WriteRegister(address, value)
	})
}

func (c *ResilientModbusClient) WriteRegisters(address uint16, values []uint16) error {
	return c.withReconnect(func() error {
		return c.client.WriteRegisters(address, values)
	})
}

func (c *ResilientModbusClient) withReconnect(op func() error) error {
	err := op()
	if err == nil || !isRecoverableModbusConnectionError(err) {
		return err
	}

	log.Printf("Modbus connection error detected, reconnecting: %v", err)
	_ = c.client.Close()
	time.Sleep(250 * time.Millisecond)
	if err2 := c.client.Open(); err2 != nil {
		return fmt.Errorf("%w (reconnect failed: %v)", err, err2)
	}

	return op()
}

func isRecoverableModbusConnectionError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	for _, token := range []string{
		"broken pipe",
		"connection reset",
		"connection refused",
		"use of closed network connection",
		"unexpected eof",
		"eof",
	} {
		if strings.Contains(msg, token) {
			return true
		}
	}
	return false
}
