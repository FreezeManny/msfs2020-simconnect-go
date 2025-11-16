package main

import (
    "bufio"
    "fmt"
    "os"
    "os/signal"
    "strconv"
    "strings"
    "syscall"
    "time"
    "unsafe"

    "github.com/grumpypixel/msfs2020-simconnect-go/examples/02_simmate/simconnect"
)

type SimVar struct {
	DefineID   simconnect.DWord
	Name, Unit string
}

var (
	requestDataInterval = time.Millisecond * 100
	receiveDataInterval = time.Millisecond * 1
	simConnect          *simconnect.SimConnect
	simVars             []*SimVar
	lastValues          = make(map[simconnect.DWord]float64)
)
func main() {
	additionalSearchPath := ""
	args := os.Args
	if len(args) > 1 {
		additionalSearchPath = args[1]
		fmt.Println("searchpath", additionalSearchPath)
	}

	if err := simconnect.Initialize(additionalSearchPath); err != nil {
		panic(err)
	}

	simConnect = simconnect.NewSimConnect()
	if err := simConnect.Open("COM Example"); err != nil {
		panic(err)
	}

	simVars = make([]*SimVar, 0)
	nameUnitMapping := map[string]string{
		"COM ACTIVE FREQUENCY:1": "MHz",
		"COM STANDBY FREQUENCY:1": "MHz",
		"COM ACTIVE FREQUENCY:2": "MHz",
		"COM STANDBY FREQUENCY:2": "MHz",
	}
	for name, unit := range nameUnitMapping {
		defineID := simconnect.NewDefineID()
		simConnect.AddToDataDefinition(defineID, name, unit, simconnect.DataTypeFloat64)
		simVars = append(simVars, &SimVar{defineID, name, unit})
	}

	done := make(chan bool, 1)
	defer close(done)
	go HandleTerminationSignal(done)
	go HandleEvents(done)

	// Set COM1 Standby via command line
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter new COM1 Standby frequency (MHz, e.g. 123.450): ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	freq, err := strconv.ParseFloat(input, 64)
	if err == nil {
		// COM_STBY_RADIO_SET_HZ expects frequency in Hz as a plain integer
		// Example: 123.450 MHz -> 123450000 Hz
		freqHz := uint32(freq * 1000000)

        // Map client event to sim event
        eventID := simconnect.NewEventID()
        groupID := simconnect.DWord(0) // Use a simple group ID instead of NewGroupID
        
        simConnect.MapClientEventToSimEvent(eventID, "COM_STBY_RADIO_SET_HZ")
        simConnect.AddClientEventToNotificationGroup(groupID, eventID, false)
        simConnect.SetNotificationGroupPriority(groupID, simconnect.GroupPriorityHighest)

        // Transmit the event with frequency in Hz
        err := simConnect.TransmitClientEvent(
            uint32(simconnect.ObjectIDUser),
            uint32(eventID),
            simconnect.DWord(freqHz),
            groupID,
            simconnect.EventFlagGroupIDIsPriority,
        )
        if err != nil {
            fmt.Printf("Failed to set COM1 Standby: %v\n", err)
        } else {
            fmt.Printf("Set COM1 Standby to %.3f MHz (%d Hz)\n", freq, freqHz)
        }
    } else {
        fmt.Println("Invalid frequency input, skipping set.")
    }

	<-done

	if err := simConnect.Close(); err != nil {
		panic(err)
	}
}

func HandleTerminationSignal(done chan bool) {
	sigterm := make(chan os.Signal, 1)
	defer close(sigterm)

	signal.Notify(sigterm, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case <-sigterm:
			done <- true
			return
		}
	}
}

func HandleEvents(done chan bool) {
	reqDataTicker := time.NewTicker(requestDataInterval)
	defer reqDataTicker.Stop()

	recvDataTicker := time.NewTicker(receiveDataInterval)
	defer recvDataTicker.Stop()

	var simObjectType = simconnect.SimObjectTypeUser
	var radius = simconnect.DWordZero

	for {
		select {
		case <-reqDataTicker.C:
			for _, simVar := range simVars {
				simConnect.RequestDataOnSimObjectType(simconnect.NewRequestID(), simVar.DefineID, radius, simObjectType)
			}

		case <-recvDataTicker.C:
			ppData, r1, err := simConnect.GetNextDispatch()
			if r1 < 0 {
				if uint32(r1) != simconnect.EFail {
					fmt.Printf("GetNextDispatch error: %d %s\n", r1, err)
					return
				}
				if ppData == nil {
					break
				}
			}

			recv := *(*simconnect.Recv)(ppData)
			switch recv.ID {
			case simconnect.RecvIDOpen:
				fmt.Println("Connected.")

			case simconnect.RecvIDQuit:
				fmt.Println("Disconnected.")
				done <- true

			case simconnect.RecvIDException:
				recvException := *(*simconnect.RecvException)(ppData)
				fmt.Println("Exception:", recvException.Exception)

			case simconnect.RecvIDSimObjectDataByType:
				data := *(*simconnect.RecvSimObjectDataByType)(ppData)
				for _, simVar := range simVars {
					if simVar.DefineID == data.DefineID {
						val := *(*float64)(unsafe.Pointer(uintptr(ppData) + unsafe.Sizeof(data)))
						lastVal, ok := lastValues[simVar.DefineID]
						if !ok || val != lastVal {
							fmt.Printf("[%d] %s %s %.3f\n", data.RequestID, simVar.Name, simVar.Unit, val)
							lastValues[simVar.DefineID] = val
						}
						break
					}
				}
			}
		}
	}
}
