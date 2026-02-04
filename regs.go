package main

import (
	"fmt"
	"log"
	"strconv"

	"github.com/simonvetter/modbus"
	"github.com/prometheus/client_golang/prometheus"
)

// Addresses and layouts per FU_DOC_TCP_CS40
const (
	AddrFactDeviceID = 0
	AddrFactSerialNum = 1 // +1
	AddrFactEthernetMAC = 3 // 3 registers (3*uint16)
	AddrFactHWRevision = 6 // +1
	AddrFirmRevision = 8 // +1
	AddrSysBuildNumber = 10 // +1
	AddrSysRegmapVersion = 12 // +1
	AddrSysOptions = 14
	AddrFutConfig = 15
	AddrFutMode = 16 // +1
	AddrFutError = 18 // +1
	AddrFutWarning = 20 // +1

	AddrFutTempAmbient = 30
	AddrFutTempFresh = 31
	AddrFutTempIndoor = 32
	AddrFutTempWaste = 33
	AddrFutHumiAmbient = 34
	AddrFutHumiFresh = 35
	AddrFutHumiIndoor = 36
	AddrFutHumiWaste = 37
	AddrFutTOut = 38

	AddrFutFilterWear = 40
	AddrPowerConsumption = 41
	AddrHeatRecovering = 42
	AddrHeatingPower = 43
	AddrAirFlow = 44
	AddrFanPWMSupply = 45
	AddrFanPWMExhaust = 46
	AddrFanRPMSupply = 47
	AddrFanRPMExhaust = 48
	AddrUin1Voltage = 49
	AddrUin2Voltage = 50
	AddrDigInputs = 51
	AddrSysBatteryVoltage = 52

	AddrMBDevStatReads = 60 // +1
	AddrMBDevStatWrites = 62 // +1
	AddrMBDevStatFails = 64 // +1
	AddrMBDevConnectedMkUI = 66
	AddrMBDevConnectedMkSens = 67 // +1
	AddrMBDevConnectedCoolBreeze = 69
	AddrMBDevConnectedValveSupply = 70 // +1
	AddrMBDevConnectedValveExhaust = 72 // +1
	AddrMBDevConnectedButton = 74
	AddrMBDevConnectedAlfa = 75

	AddrVzvIdentify = 80

	// UI wall controllers start at 100, then 105,110 (3 units)
	AddrUIBase = 100
	UIInstances = 3

	// Wall sensors (1-8) start at 115 and step by 5
	AddrSensBase = 115
	SensInstances = 8

	// ALFA controllers base at 160 stepping by 10 up to 230
	AddrAlfaBase = 160
	AlfaInstances = 8

	// External sensors (1-8) at 300+ stepping by 10
	AddrExtSensBase = 300
	ExtSensInstances = 8
)

// Holding registry addresses (for reference)
const (
	AddrHoldingFuncVentilation = 0
	AddrHoldingFuncBoostTm = 1
	AddrHoldingFuncCirculationTm = 2
	AddrHoldingFuncOverpressureTm = 3
	AddrHoldingFuncNightTm = 4
	AddrHoldingFuncPartyTm = 5
	AddrHoldingFuncAwayBegin = 6
	AddrHoldingFuncAwayEnd = 8
	AddrHoldingCfgTempSet = 10
	AddrHoldingCfgHumiSet = 11
	AddrHoldingFuncTimeProg = 12
	AddrHoldingFuncAntiradon = 13
	AddrHoldingCfgBypassEnable = 14
	AddrHoldingCfgHeatingEnable = 15
	AddrHoldingCfgCoolingEnable = 16
	AddrHoldingCfgComfortEnable = 17
	AddrHoldingVzvCBPriorityControl = 20
	AddrHoldingVzvKitchenhoodNormallyOpen = 21
	AddrHoldingVzvBoostVolumePerRun = 22
	AddrHoldingVzvKitchenhoodNormallyOpenVolume = 23

	// UI corrections base at 100 stepping by 5
	AddrHoldingUITempCorrBase = 100
	HoldingUIInstances = 3

	// External sensor corrections base at 115 stepping by 5
	AddrHoldingExtSensTempCorrBase = 115
	HoldingExtSensInstances = 8

	// ALFA corrections base at 160 stepping by 5 (temp) and 162 (ntc temp)
	AddrHoldingAlfaTempCorrBase = 160
	AddrHoldingAlfaNTCTempCorrBase = 162

	// External buttons base at 400 stepping by 10
	AddrHoldingExtBtnBase = 400
	HoldingExtBtnInstances = 8

	// Security
	AddrHoldingAccessCode = 900
	AddrHoldingUserPassword = 920
	AddrHoldingPasswordTimeout = 922
)

// InputRegs holds all relevant mapped input registers
type InputRegs struct {
	FactDeviceID uint16
	FactSerialNum uint32
	FactEthernetMAC [3]uint16
	FactHWRevision uint32
	FirmRevision uint32
	SysBuildNumber uint32
	SysRegmapVersion uint32
	SysOptions uint16
	FutConfig uint16
	FutMode uint32
	FutError uint32
	FutWarning uint32

	TempAmbient float64 // Celsius
	TempFresh float64
	TempIndoor float64
	TempWaste float64
	HumiAmbient float64 // %
	HumiFresh float64
	HumiIndoor float64
	HumiWaste float64
	TOut float64

	FilterWear uint16
	PowerConsumption uint16
	HeatRecovering uint16
	HeatingPower uint16
	AirFlow uint16
	FanPWMSupply uint16
	FanPWMExhaust uint16
	FanRPMSupply uint16
	FanRPMExhaust uint16
	Uin1Voltage uint16
	Uin2Voltage uint16
	DigInputs uint16
	SysBatteryVoltage uint16

	MBDevStatReads uint32
	MBDevStatWrites uint32
	MBDevStatFails uint32
	MBDevConnectedMkUI uint16
	MBDevConnectedMkSens uint32
	MBDevConnectedCoolBreeze uint16
	MBDevConnectedValveSupply uint32
	MBDevConnectedValveExhaust uint32
	MBDevConnectedButton uint16
	MBDevConnectedAlfa uint16

	VzvIdentify uint16

	UIAddress [UIInstances]uint16
	UIOptions [UIInstances]uint16
	UICo2 [UIInstances]uint16
	UITemp [UIInstances]float64
	UIHumi [UIInstances]float64

	SensMBAddress [SensInstances]uint16
	SensOptions [SensInstances]uint16
	SensCo2 [SensInstances]uint16
	SensTemp [SensInstances]float64
	SensHumi [SensInstances]float64

	AlfaMBAddress [AlfaInstances]uint16
	AlfaOptions [AlfaInstances]uint16
	AlfaCo2 [AlfaInstances]uint16
	AlfaTemp [AlfaInstances]float64
	AlfaHumi [AlfaInstances]float64
	AlfaNTCTemp [AlfaInstances]float64

	ExtSensPresent [ExtSensInstances]uint16
	ExtSensInvalidate [ExtSensInstances]uint16
	ExtSensTemp [ExtSensInstances]float64
	ExtSensRH [ExtSensInstances]float64
	ExtSensCo2 [ExtSensInstances]uint16
	ExtSensTFloor [ExtSensInstances]float64
}

// HoldingRegs holds all writable (holding) registers
type HoldingRegs struct {
	FuncVentilation uint16 // 0-6
	FuncBoostTm uint16 // seconds
	FuncCirculationTm uint16
	FuncOverpressureTm uint16
	FuncNightTm uint16
	FuncPartyTm uint16
	FuncAwayBegin uint32
	FuncAwayEnd uint32
	CfgTempSet float64 // 0.1°C
	CfgHumiSet float64 // 0.1%
	FuncTimeProg uint16 // 0/1
	FuncAntiradon uint16 // 0/1
	CfgBypassEnable uint16 // 0/1
	CfgHeatingEnable uint16 // 0/1
	CfgCoolingEnable uint16 // 0/1
	CfgComfortEnable uint16 // 0/1
	VzvCBPriorityControl uint16 // 0/1
	VzvKitchenhoodNormallyOpen uint16 // 0/1
	VzvBoostVolumePerRun uint16 // m3/h
	VzvKitchenhoodNormallyOpenVolume uint16 // m3/h

	UITempCorr [HoldingUIInstances]float64 // 0.1°C
	ExtSensTempCorr [HoldingExtSensInstances]float64 // 0.1°C
	AlfaTempCorr [AlfaInstances]float64 // 0.1°C
	AlfaNTCTempCorr [AlfaInstances]float64 // 0.1°C

	ExtBtnPresent [HoldingExtBtnInstances]uint16 // 0/1
	ExtBtnMode [HoldingExtBtnInstances]uint16 // 0=boost, 1=hood
	ExtBtnTm [HoldingExtBtnInstances]uint16 // seconds
	ExtBtnActive [HoldingExtBtnInstances]uint16 // 0/1

	AccessCode uint16
	UserPassword uint16
	PasswordTimeout uint16
}

// simple helper to safely read address from map
func u16(m map[uint16]uint16, addr uint16) uint16 { return m[addr] }

func u32(m map[uint16]uint16, addr uint16) uint32 {
	hi := uint32(m[addr])
	lo := uint32(m[addr+1])
	return (hi << 16) | lo
}

func i16f(m map[uint16]uint16, addr uint16, scale float64) float64 {
	v := int16(m[addr])
	return float64(v) * scale
}

func u16f(m map[uint16]uint16, addr uint16, scale float64) float64 {
	return float64(m[addr]) * scale
}

// DecodeInputMap constructs InputRegs from a map[address]value
func DecodeInputMap(m map[uint16]uint16) InputRegs {
	r := InputRegs{}
	// device & system
	r.FactDeviceID = u16(m, AddrFactDeviceID)
	r.FactSerialNum = u32(m, AddrFactSerialNum)
	for i := 0; i < 3; i++ {
		r.FactEthernetMAC[i] = u16(m, AddrFactEthernetMAC+uint16(i))
	}
	r.FactHWRevision = u32(m, AddrFactHWRevision)
	r.FirmRevision = u32(m, AddrFirmRevision)
	r.SysBuildNumber = u32(m, AddrSysBuildNumber)
	r.SysRegmapVersion = u32(m, AddrSysRegmapVersion)
	r.SysOptions = u16(m, AddrSysOptions)
	r.FutConfig = u16(m, AddrFutConfig)
	r.FutMode = u32(m, AddrFutMode)
	r.FutError = u32(m, AddrFutError)
	r.FutWarning = u32(m, AddrFutWarning)

	// temps & humi (scale 0.1)
	r.TempAmbient = i16f(m, AddrFutTempAmbient, 0.1)
	r.TempFresh = i16f(m, AddrFutTempFresh, 0.1)
	r.TempIndoor = i16f(m, AddrFutTempIndoor, 0.1)
	r.TempWaste = i16f(m, AddrFutTempWaste, 0.1)
	r.HumiAmbient = i16f(m, AddrFutHumiAmbient, 0.1)
	r.HumiFresh = i16f(m, AddrFutHumiFresh, 0.1)
	r.HumiIndoor = i16f(m, AddrFutHumiIndoor, 0.1)
	r.HumiWaste = i16f(m, AddrFutHumiWaste, 0.1)
	r.TOut = i16f(m, AddrFutTOut, 0.1)

	// misc
	r.FilterWear = u16(m, AddrFutFilterWear)
	r.PowerConsumption = u16(m, AddrPowerConsumption)
	r.HeatRecovering = u16(m, AddrHeatRecovering)
	r.HeatingPower = u16(m, AddrHeatingPower)
	r.AirFlow = u16(m, AddrAirFlow)
	r.FanPWMSupply = u16(m, AddrFanPWMSupply)
	r.FanPWMExhaust = u16(m, AddrFanPWMExhaust)
	r.FanRPMSupply = u16(m, AddrFanRPMSupply)
	r.FanRPMExhaust = u16(m, AddrFanRPMExhaust)
	r.Uin1Voltage = u16(m, AddrUin1Voltage)
	r.Uin2Voltage = u16(m, AddrUin2Voltage)
	r.DigInputs = u16(m, AddrDigInputs)
	r.SysBatteryVoltage = u16(m, AddrSysBatteryVoltage)

	// stats
	r.MBDevStatReads = u32(m, AddrMBDevStatReads)
	r.MBDevStatWrites = u32(m, AddrMBDevStatWrites)
	r.MBDevStatFails = u32(m, AddrMBDevStatFails)
	r.MBDevConnectedMkUI = u16(m, AddrMBDevConnectedMkUI)
	r.MBDevConnectedMkSens = u32(m, AddrMBDevConnectedMkSens)
	r.MBDevConnectedCoolBreeze = u16(m, AddrMBDevConnectedCoolBreeze)
	r.MBDevConnectedValveSupply = u32(m, AddrMBDevConnectedValveSupply)
	r.MBDevConnectedValveExhaust = u32(m, AddrMBDevConnectedValveExhaust)
	r.MBDevConnectedButton = u16(m, AddrMBDevConnectedButton)
	r.MBDevConnectedAlfa = u16(m, AddrMBDevConnectedAlfa)

	r.VzvIdentify = u16(m, AddrVzvIdentify)

	// UI instances
	for i := 0; i < UIInstances; i++ {
		base := AddrUIBase + uint16(i*5)
		r.UIAddress[i] = u16(m, base)
		r.UIOptions[i] = u16(m, base+1)
		r.UICo2[i] = u16(m, base+2)
		r.UITemp[i] = i16f(m, base+3, 0.1)
		r.UIHumi[i] = u16f(m, base+4, 0.1)
	}

	// sensors 1..8 (step 5)
	for i := 0; i < SensInstances; i++ {
		base := AddrSensBase + uint16(i*5)
		r.SensMBAddress[i] = u16(m, base)
		r.SensOptions[i] = u16(m, base+1)
		r.SensCo2[i] = u16(m, base+2)
		r.SensTemp[i] = i16f(m, base+3, 0.1)
		r.SensHumi[i] = u16f(m, base+4, 0.1)
	}

	// ALFA controllers (step 10)
	for i := 0; i < AlfaInstances; i++ {
		base := AddrAlfaBase + uint16(i*10)
		r.AlfaMBAddress[i] = u16(m, base)
		r.AlfaOptions[i] = u16(m, base+1)
		r.AlfaCo2[i] = u16(m, base+2)
		r.AlfaTemp[i] = i16f(m, base+3, 0.1)
		r.AlfaHumi[i] = u16f(m, base+4, 0.1)
		r.AlfaNTCTemp[i] = u16f(m, base+5, 0.1)
	}

	// External sensors (step 10 from 300: 300-305, 310-315, ..., 370-375)
	for i := 0; i < ExtSensInstances; i++ {
		base := AddrExtSensBase + uint16(i*10)
		r.ExtSensPresent[i] = u16(m, base)
		r.ExtSensInvalidate[i] = u16(m, base+1)
		r.ExtSensTemp[i] = i16f(m, base+2, 0.1)
		r.ExtSensRH[i] = u16f(m, base+3, 1.0)
		r.ExtSensCo2[i] = u16(m, base+4)
		r.ExtSensTFloor[i] = i16f(m, base+5, 0.1)
	}

	return r
}

// DecodeHoldingMap constructs HoldingRegs from a map[address]value
func DecodeHoldingMap(m map[uint16]uint16) HoldingRegs {
	r := HoldingRegs{}

	r.FuncVentilation = u16(m, AddrHoldingFuncVentilation)
	r.FuncBoostTm = u16(m, AddrHoldingFuncBoostTm)
	r.FuncCirculationTm = u16(m, AddrHoldingFuncCirculationTm)
	r.FuncOverpressureTm = u16(m, AddrHoldingFuncOverpressureTm)
	r.FuncNightTm = u16(m, AddrHoldingFuncNightTm)
	r.FuncPartyTm = u16(m, AddrHoldingFuncPartyTm)
	r.FuncAwayBegin = u32(m, AddrHoldingFuncAwayBegin)
	r.FuncAwayEnd = u32(m, AddrHoldingFuncAwayEnd)
	r.CfgTempSet = i16f(m, AddrHoldingCfgTempSet, 0.1)
	r.CfgHumiSet = u16f(m, AddrHoldingCfgHumiSet, 0.1)
	r.FuncTimeProg = u16(m, AddrHoldingFuncTimeProg)
	r.FuncAntiradon = u16(m, AddrHoldingFuncAntiradon)
	r.CfgBypassEnable = u16(m, AddrHoldingCfgBypassEnable)
	r.CfgHeatingEnable = u16(m, AddrHoldingCfgHeatingEnable)
	r.CfgCoolingEnable = u16(m, AddrHoldingCfgCoolingEnable)
	r.CfgComfortEnable = u16(m, AddrHoldingCfgComfortEnable)
	r.VzvCBPriorityControl = u16(m, AddrHoldingVzvCBPriorityControl)
	r.VzvKitchenhoodNormallyOpen = u16(m, AddrHoldingVzvKitchenhoodNormallyOpen)
	r.VzvBoostVolumePerRun = u16(m, AddrHoldingVzvBoostVolumePerRun)
	r.VzvKitchenhoodNormallyOpenVolume = u16(m, AddrHoldingVzvKitchenhoodNormallyOpenVolume)

	// UI temp corrections
	for i := 0; i < HoldingUIInstances; i++ {
		addr := AddrHoldingUITempCorrBase + uint16(i*5)
		r.UITempCorr[i] = i16f(m, addr, 0.1)
	}

	// External sensor temp corrections
	for i := 0; i < HoldingExtSensInstances; i++ {
		addr := AddrHoldingExtSensTempCorrBase + uint16(i*5)
		r.ExtSensTempCorr[i] = i16f(m, addr, 0.1)
	}

	// ALFA temp corrections
	for i := 0; i < AlfaInstances; i++ {
		r.AlfaTempCorr[i] = i16f(m, AddrHoldingAlfaTempCorrBase+uint16(i*5), 0.1)
		r.AlfaNTCTempCorr[i] = i16f(m, AddrHoldingAlfaNTCTempCorrBase+uint16(i*5), 0.1)
	}

	// External buttons
	for i := 0; i < HoldingExtBtnInstances; i++ {
		base := AddrHoldingExtBtnBase + uint16(i*10)
		r.ExtBtnPresent[i] = u16(m, base)
		r.ExtBtnMode[i] = u16(m, base+1)
		r.ExtBtnTm[i] = u16(m, base+2)
		r.ExtBtnActive[i] = u16(m, base+3)
	}

	r.AccessCode = u16(m, AddrHoldingAccessCode)
	r.UserPassword = u16(m, AddrHoldingUserPassword)
	r.PasswordTimeout = u16(m, AddrHoldingPasswordTimeout)

	return r
}

// EncodeHoldingRegs converts HoldingRegs back to map[uint16]uint16 for writing
func EncodeHoldingRegs(r HoldingRegs) map[uint16]uint16 {
	m := make(map[uint16]uint16)

	m[AddrHoldingFuncVentilation] = r.FuncVentilation
	m[AddrHoldingFuncBoostTm] = r.FuncBoostTm
	m[AddrHoldingFuncCirculationTm] = r.FuncCirculationTm
	m[AddrHoldingFuncOverpressureTm] = r.FuncOverpressureTm
	m[AddrHoldingFuncNightTm] = r.FuncNightTm
	m[AddrHoldingFuncPartyTm] = r.FuncPartyTm

	// uint32 fields (split into two uint16s)
	hi, lo := splitU32(r.FuncAwayBegin)
	m[AddrHoldingFuncAwayBegin] = hi
	m[AddrHoldingFuncAwayBegin+1] = lo
	hi, lo = splitU32(r.FuncAwayEnd)
	m[AddrHoldingFuncAwayEnd] = hi
	m[AddrHoldingFuncAwayEnd+1] = lo

	m[AddrHoldingCfgTempSet] = uint16(int16(r.CfgTempSet / 0.1))
	m[AddrHoldingCfgHumiSet] = uint16(r.CfgHumiSet / 0.1)
	m[AddrHoldingFuncTimeProg] = r.FuncTimeProg
	m[AddrHoldingFuncAntiradon] = r.FuncAntiradon
	m[AddrHoldingCfgBypassEnable] = r.CfgBypassEnable
	m[AddrHoldingCfgHeatingEnable] = r.CfgHeatingEnable
	m[AddrHoldingCfgCoolingEnable] = r.CfgCoolingEnable
	m[AddrHoldingCfgComfortEnable] = r.CfgComfortEnable
	m[AddrHoldingVzvCBPriorityControl] = r.VzvCBPriorityControl
	m[AddrHoldingVzvKitchenhoodNormallyOpen] = r.VzvKitchenhoodNormallyOpen
	m[AddrHoldingVzvBoostVolumePerRun] = r.VzvBoostVolumePerRun
	m[AddrHoldingVzvKitchenhoodNormallyOpenVolume] = r.VzvKitchenhoodNormallyOpenVolume

	// UI temp corrections
	for i := 0; i < HoldingUIInstances; i++ {
		addr := AddrHoldingUITempCorrBase + uint16(i*5)
		m[addr] = uint16(int16(r.UITempCorr[i] / 0.1))
	}

	// External sensor temp corrections
	for i := 0; i < HoldingExtSensInstances; i++ {
		addr := AddrHoldingExtSensTempCorrBase + uint16(i*5)
		m[addr] = uint16(int16(r.ExtSensTempCorr[i] / 0.1))
	}

	// ALFA temp corrections
	for i := 0; i < AlfaInstances; i++ {
		m[AddrHoldingAlfaTempCorrBase+uint16(i*5)] = uint16(int16(r.AlfaTempCorr[i] / 0.1))
		m[AddrHoldingAlfaNTCTempCorrBase+uint16(i*5)] = uint16(int16(r.AlfaNTCTempCorr[i] / 0.1))
	}

	// External buttons
	for i := 0; i < HoldingExtBtnInstances; i++ {
		base := AddrHoldingExtBtnBase + uint16(i*10)
		m[base] = r.ExtBtnPresent[i]
		m[base+1] = r.ExtBtnMode[i]
		m[base+2] = r.ExtBtnTm[i]
		m[base+3] = r.ExtBtnActive[i]
	}

	m[AddrHoldingAccessCode] = r.AccessCode
	m[AddrHoldingUserPassword] = r.UserPassword
	m[AddrHoldingPasswordTimeout] = r.PasswordTimeout

	return m
}

// splitU32 splits a uint32 into high and low uint16
func splitU32(v uint32) (uint16, uint16) {
	return uint16(v >> 16), uint16(v & 0xFFFF)
}

// WriteFieldSpec describes a writable field (addr, scale, register count)
type WriteFieldSpec struct {
	Addr     uint16
	Scale    float64 // multiplier to convert float -> register value (value/Scale -> encoded integer)
	RegCount int     // number of registers used (1 or 2)
}

// WriteableFields lists fields that may be written via single-register writes
var WriteableFields = map[string]WriteFieldSpec{
	"FuncVentilation":                {Addr: AddrHoldingFuncVentilation, Scale: 1.0, RegCount: 1},
	"FuncBoostTm":                   {Addr: AddrHoldingFuncBoostTm, Scale: 1.0, RegCount: 1},
	"FuncCirculationTm":             {Addr: AddrHoldingFuncCirculationTm, Scale: 1.0, RegCount: 1},
	"FuncOverpressureTm":            {Addr: AddrHoldingFuncOverpressureTm, Scale: 1.0, RegCount: 1},
	"FuncNightTm":                   {Addr: AddrHoldingFuncNightTm, Scale: 1.0, RegCount: 1},
	"FuncPartyTm":                   {Addr: AddrHoldingFuncPartyTm, Scale: 1.0, RegCount: 1},
	"CfgTempSet":                    {Addr: AddrHoldingCfgTempSet, Scale: 0.1, RegCount: 1},
	"CfgHumiSet":                    {Addr: AddrHoldingCfgHumiSet, Scale: 0.1, RegCount: 1},
	"FuncTimeProg":                  {Addr: AddrHoldingFuncTimeProg, Scale: 1.0, RegCount: 1},
	"FuncAntiradon":                 {Addr: AddrHoldingFuncAntiradon, Scale: 1.0, RegCount: 1},
	"CfgBypassEnable":               {Addr: AddrHoldingCfgBypassEnable, Scale: 1.0, RegCount: 1},
	"CfgHeatingEnable":              {Addr: AddrHoldingCfgHeatingEnable, Scale: 1.0, RegCount: 1},
	"CfgCoolingEnable":              {Addr: AddrHoldingCfgCoolingEnable, Scale: 1.0, RegCount: 1},
	"CfgComfortEnable":              {Addr: AddrHoldingCfgComfortEnable, Scale: 1.0, RegCount: 1},
	"VzvCBPriorityControl":          {Addr: AddrHoldingVzvCBPriorityControl, Scale: 1.0, RegCount: 1},
	"VzvKitchenhoodNormallyOpen":    {Addr: AddrHoldingVzvKitchenhoodNormallyOpen, Scale: 1.0, RegCount: 1},
	"VzvBoostVolumePerRun":          {Addr: AddrHoldingVzvBoostVolumePerRun, Scale: 1.0, RegCount: 1},
	"VzvKitchenhoodNormallyOpenVolume": {Addr: AddrHoldingVzvKitchenhoodNormallyOpenVolume, Scale: 1.0, RegCount: 1},

	// External sensor temperature corrections (1..8)
	"ExtSensTempCorr1": {Addr: AddrHoldingExtSensTempCorrBase + 0, Scale: 0.1, RegCount: 1},
	"ExtSensTempCorr2": {Addr: AddrHoldingExtSensTempCorrBase + 5, Scale: 0.1, RegCount: 1},
	"ExtSensTempCorr3": {Addr: AddrHoldingExtSensTempCorrBase + 10, Scale: 0.1, RegCount: 1},
	"ExtSensTempCorr4": {Addr: AddrHoldingExtSensTempCorrBase + 15, Scale: 0.1, RegCount: 1},
	"ExtSensTempCorr5": {Addr: AddrHoldingExtSensTempCorrBase + 20, Scale: 0.1, RegCount: 1},
	"ExtSensTempCorr6": {Addr: AddrHoldingExtSensTempCorrBase + 25, Scale: 0.1, RegCount: 1},
	"ExtSensTempCorr7": {Addr: AddrHoldingExtSensTempCorrBase + 30, Scale: 0.1, RegCount: 1},
	"ExtSensTempCorr8": {Addr: AddrHoldingExtSensTempCorrBase + 35, Scale: 0.1, RegCount: 1},

	// External sensor present/invalidate (addresses mirror input ext sensors at 300+, step 10)
	"ExtSensPresent1": {Addr: AddrExtSensBase + 0, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate1": {Addr: AddrExtSensBase + 1, Scale: 1.0, RegCount: 1},
	"ExtSensPresent2": {Addr: AddrExtSensBase + 10, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate2": {Addr: AddrExtSensBase + 11, Scale: 1.0, RegCount: 1},
	"ExtSensPresent3": {Addr: AddrExtSensBase + 20, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate3": {Addr: AddrExtSensBase + 21, Scale: 1.0, RegCount: 1},
	"ExtSensPresent4": {Addr: AddrExtSensBase + 30, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate4": {Addr: AddrExtSensBase + 31, Scale: 1.0, RegCount: 1},
	"ExtSensPresent5": {Addr: AddrExtSensBase + 40, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate5": {Addr: AddrExtSensBase + 41, Scale: 1.0, RegCount: 1},
	"ExtSensPresent6": {Addr: AddrExtSensBase + 50, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate6": {Addr: AddrExtSensBase + 51, Scale: 1.0, RegCount: 1},
	"ExtSensPresent7": {Addr: AddrExtSensBase + 60, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate7": {Addr: AddrExtSensBase + 61, Scale: 1.0, RegCount: 1},
	"ExtSensPresent8": {Addr: AddrExtSensBase + 70, Scale: 1.0, RegCount: 1},
	"ExtSensInvalidate8": {Addr: AddrExtSensBase + 71, Scale: 1.0, RegCount: 1},

	// Allow writing live external sensor readings (for testing)
	// For each sensor N (1..8) addresses are AddrExtSensBase + (N-1)*10 + offset
	"ExtSensTemp1": {Addr: AddrExtSensBase + 2, Scale: 0.1, RegCount: 1},
	"ExtSensRH1": {Addr: AddrExtSensBase + 3, Scale: 1.0, RegCount: 1},
	"ExtSensCo21": {Addr: AddrExtSensBase + 4, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor1": {Addr: AddrExtSensBase + 5, Scale: 0.1, RegCount: 1},

	"ExtSensTemp2": {Addr: AddrExtSensBase + 12, Scale: 0.1, RegCount: 1},
	"ExtSensRH2": {Addr: AddrExtSensBase + 13, Scale: 1.0, RegCount: 1},
	"ExtSensCo22": {Addr: AddrExtSensBase + 14, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor2": {Addr: AddrExtSensBase + 15, Scale: 0.1, RegCount: 1},

	"ExtSensTemp3": {Addr: AddrExtSensBase + 22, Scale: 0.1, RegCount: 1},
	"ExtSensRH3": {Addr: AddrExtSensBase + 23, Scale: 1.0, RegCount: 1},
	"ExtSensCo23": {Addr: AddrExtSensBase + 24, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor3": {Addr: AddrExtSensBase + 25, Scale: 0.1, RegCount: 1},

	"ExtSensTemp4": {Addr: AddrExtSensBase + 32, Scale: 0.1, RegCount: 1},
	"ExtSensRH4": {Addr: AddrExtSensBase + 33, Scale: 1.0, RegCount: 1},
	"ExtSensCo24": {Addr: AddrExtSensBase + 34, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor4": {Addr: AddrExtSensBase + 35, Scale: 0.1, RegCount: 1},

	"ExtSensTemp5": {Addr: AddrExtSensBase + 42, Scale: 0.1, RegCount: 1},
	"ExtSensRH5": {Addr: AddrExtSensBase + 43, Scale: 1.0, RegCount: 1},
	"ExtSensCo25": {Addr: AddrExtSensBase + 44, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor5": {Addr: AddrExtSensBase + 45, Scale: 0.1, RegCount: 1},

	"ExtSensTemp6": {Addr: AddrExtSensBase + 52, Scale: 0.1, RegCount: 1},
	"ExtSensRH6": {Addr: AddrExtSensBase + 53, Scale: 1.0, RegCount: 1},
	"ExtSensCo26": {Addr: AddrExtSensBase + 54, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor6": {Addr: AddrExtSensBase + 55, Scale: 0.1, RegCount: 1},

	"ExtSensTemp7": {Addr: AddrExtSensBase + 62, Scale: 0.1, RegCount: 1},
	"ExtSensRH7": {Addr: AddrExtSensBase + 63, Scale: 1.0, RegCount: 1},
	"ExtSensCo27": {Addr: AddrExtSensBase + 64, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor7": {Addr: AddrExtSensBase + 65, Scale: 0.1, RegCount: 1},

	"ExtSensTemp8": {Addr: AddrExtSensBase + 72, Scale: 0.1, RegCount: 1},
	"ExtSensRH8": {Addr: AddrExtSensBase + 73, Scale: 1.0, RegCount: 1},
	"ExtSensCo28": {Addr: AddrExtSensBase + 74, Scale: 1.0, RegCount: 1},
	"ExtSensTFloor8": {Addr: AddrExtSensBase + 75, Scale: 0.1, RegCount: 1},
}

// WriteSingleRegister performs a single-register write for a named field
func WriteSingleRegister(client *modbus.ModbusClient, name string, value float64) error {
	spec, ok := WriteableFields[name]
	if !ok {
		return fmt.Errorf("unknown or not-writable field: %s", name)
	}
	if spec.RegCount != 1 {
		return fmt.Errorf("field %s requires %d registers; single-register write not supported", name, spec.RegCount)
	}

	// convert value according to scale
	var encoded uint16
	if spec.Scale == 0 {
		return fmt.Errorf("invalid scale for field %s", name)
	}
	// For signed values (like temperatures) we store as int16; treat values that fit in int16
	scaled := int64(value / spec.Scale)
	if scaled < -0x8000 || scaled > 0xFFFF {
		return fmt.Errorf("value out of range for field %s", name)
	}
	encoded = uint16(int16(scaled))

	log.Printf("WriteSingleRegister: %s -> %v (addr %d, encoded 0x%04X)", name, value, spec.Addr, encoded)
	if err := client.WriteRegister(spec.Addr, encoded); err != nil {
		return fmt.Errorf("write register %d: %w", spec.Addr, err)
	}
	log.Printf("WriteSingleRegister success: %s (addr %d, encoded 0x%04X)", name, spec.Addr, encoded)
	return nil
}

// ------------------ Prometheus metrics ------------------

var (
	regGauges = map[string]prometheus.Gauge{}
	regGaugeVecs = map[string]*prometheus.GaugeVec{}
)

func RegisterRegMetrics() {
	// Basic single-value gauges
	addGauge("fut_temp_ambient_celsius", "Ambient temperature (°C)")
	addGauge("fut_temp_fresh_celsius", "Fresh air temperature (°C)")
	addGauge("fut_temp_indoor_celsius", "Indoor temperature (°C)")
	addGauge("fut_temp_waste_celsius", "Waste air temperature (°C)")

	addGauge("fut_humi_ambient_percent", "Ambient humidity (%)")
	addGauge("fut_humi_fresh_percent", "Fresh air humidity (%)")
	addGauge("fut_humi_indoor_percent", "Indoor humidity (%)")
	addGauge("fut_humi_waste_percent", "Waste humidity (%)")

	addGauge("fut_filter_wear_percent", "Filter wear (%)")
	addGauge("fut_power_consumption_watts", "Power consumption (W)")
	addGauge("fut_heat_recovering_watts", "Heat recovering (W)")
	addGauge("fut_heating_power_watts", "Heating power (W)")
	addGauge("fut_air_flow_m3h", "Air flow (m3/h)")
	addGauge("fut_fan_pwm_supply_percent", "Fan PWM supply (%)")
	addGauge("fut_fan_pwm_exhaust_percent", "Fan PWM exhaust (%)")
	addGauge("fut_fan_rpm_supply", "Fan RPM supply")
	addGauge("fut_fan_rpm_exhaust", "Fan RPM exhaust")
	addGauge("fut_uint1_voltage_mv", "UIN1 voltage (mV)")
	addGauge("fut_uint2_voltage_mv", "UIN2 voltage (mV)")

	addGaugeVec("ui_temp_celsius", "Wall controller temperature (°C)")
	addGaugeVec("ui_humi_percent", "Wall controller humidity (%)")

	addGaugeVec("sens_temp_celsius", "Sensor temperature (°C)")
	addGaugeVec("sens_humi_percent", "Sensor humidity (%)")
	addGaugeVec("alfa_temp_celsius", "ALFA temperature (°C)")
	addGaugeVec("alfa_humi_percent", "ALFA humidity (%)")
	addGaugeVec("alfa_ntc_temp_celsius", "ALFA NTC temperature (°C)")

	addGaugeVec("ext_sens_temp_celsius", "External sensor temperature (°C)")
	addGaugeVec("ext_sens_rh_percent", "External sensor relative humidity (%)")
	addGaugeVec("ext_sens_co2_ppm", "External sensor CO2 (ppm)")
	addGaugeVec("ext_sens_t_floor_celsius", "External sensor floor temperature (°C)")

	// Register all defined gauges
	for _, g := range regGauges {
		prometheus.MustRegister(g)
	}
	for _, gv := range regGaugeVecs {
		prometheus.MustRegister(gv)
	}
}

func addGauge(name, help string) {
	regGauges[name] = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: name,
		Help: help,
	})
}

func addGaugeVec(name, help string) {
	regGaugeVecs[name] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: name,
		Help: help,
	}, []string{"idx"})
}

// UpdatePrometheus updates metrics from decoded InputRegs
func UpdatePrometheus(r InputRegs) {
	setGauge("fut_temp_ambient_celsius", r.TempAmbient)
	setGauge("fut_temp_fresh_celsius", r.TempFresh)
	setGauge("fut_temp_indoor_celsius", r.TempIndoor)
	setGauge("fut_temp_waste_celsius", r.TempWaste)

	setGauge("fut_humi_ambient_percent", r.HumiAmbient)
	setGauge("fut_humi_fresh_percent", r.HumiFresh)
	setGauge("fut_humi_indoor_percent", r.HumiIndoor)
	setGauge("fut_humi_waste_percent", r.HumiWaste)

	setGauge("fut_filter_wear_percent", float64(r.FilterWear))
	setGauge("fut_power_consumption_watts", float64(r.PowerConsumption))
	setGauge("fut_heat_recovering_watts", float64(r.HeatRecovering))
	setGauge("fut_heating_power_watts", float64(r.HeatingPower))
	setGauge("fut_air_flow_m3h", float64(r.AirFlow))
	setGauge("fut_fan_pwm_supply_percent", float64(r.FanPWMSupply))
	setGauge("fut_fan_pwm_exhaust_percent", float64(r.FanPWMExhaust))
	setGauge("fut_fan_rpm_supply", float64(r.FanRPMSupply))
	setGauge("fut_fan_rpm_exhaust", float64(r.FanRPMExhaust))
	setGauge("fut_uint1_voltage_mv", float64(r.Uin1Voltage))
	setGauge("fut_uint2_voltage_mv", float64(r.Uin2Voltage))

	// UI
	for i := 0; i < UIInstances; i++ {
		idx := strconv.Itoa(i + 1)
		regGaugeVecs["ui_temp_celsius"].WithLabelValues(idx).Set(r.UITemp[i])
		regGaugeVecs["ui_humi_percent"].WithLabelValues(idx).Set(r.UIHumi[i])
	}
	// Sensors
	for i := 0; i < SensInstances; i++ {
		idx := strconv.Itoa(i + 1)
		regGaugeVecs["sens_temp_celsius"].WithLabelValues(idx).Set(r.SensTemp[i])
		regGaugeVecs["sens_humi_percent"].WithLabelValues(idx).Set(r.SensHumi[i])
	}
	// Alfa
	for i := 0; i < AlfaInstances; i++ {
		idx := strconv.Itoa(i + 1)
		regGaugeVecs["alfa_temp_celsius"].WithLabelValues(idx).Set(r.AlfaTemp[i])
		regGaugeVecs["alfa_humi_percent"].WithLabelValues(idx).Set(r.AlfaHumi[i])
		regGaugeVecs["alfa_ntc_temp_celsius"].WithLabelValues(idx).Set(r.AlfaNTCTemp[i])
	}
	// External sensors
	for i := 0; i < ExtSensInstances; i++ {
		idx := strconv.Itoa(i + 1)
		regGaugeVecs["ext_sens_temp_celsius"].WithLabelValues(idx).Set(r.ExtSensTemp[i])
		regGaugeVecs["ext_sens_rh_percent"].WithLabelValues(idx).Set(r.ExtSensRH[i])
		regGaugeVecs["ext_sens_co2_ppm"].WithLabelValues(idx).Set(float64(r.ExtSensCo2[i]))
		regGaugeVecs["ext_sens_t_floor_celsius"].WithLabelValues(idx).Set(r.ExtSensTFloor[i])
	}
}

func setGauge(name string, v float64) {
	if g, ok := regGauges[name]; ok {
		g.Set(v)
	} else {
		fmt.Printf("metric %s not found\n", name)
	}
}
