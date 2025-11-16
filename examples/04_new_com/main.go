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

    "github.com/grumpypixel/msfs2020-simconnect-go/simconnect"
)

type SimVar struct {
	DefineID   simconnect.DWord
	Name, Unit string
}

var (
	receiveDataInterval = time.Millisecond * 1 // Check for dispatches very frequently
	simConnect          *simconnect.SimConnect
	simVars             []*SimVar
	lastValues          = make(map[simconnect.DWord]float64)
	updateCounter       int
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
	
	// Setup data definitions and subscribe to automatic updates
	for name, unit := range nameUnitMapping {
		defineID := simconnect.NewDefineID()
		requestID := simconnect.NewRequestID()
		
		// Add the data definition
		simConnect.AddToDataDefinition(defineID, name, unit, simconnect.DataTypeFloat64)
		
		// Request event-driven updates: data will be pushed automatically
		// No need to poll! SimConnect will send updates when values change
		simConnect.RequestDataOnSimObject(
			requestID,
			defineID,
			simconnect.ObjectIDUser,
			simconnect.PeriodVisualFrame, // Automatic updates every visual frame (~60 FPS)
			simconnect.DWordZero,         // flags
		)
		
		simVars = append(simVars, &SimVar{defineID, name, unit})
		fmt.Printf("Subscribed to '%s' with event-driven updates (~60 FPS)\n", name)
	}

	done := make(chan bool, 1)
	defer close(done)
	go HandleTerminationSignal(done)
	go HandleEvents(done)

	fmt.Println("\n=== Event-Driven Mode (High Frequency) ===")
	fmt.Println("Data will be automatically pushed from SimConnect every visual frame (~60 FPS).")
	fmt.Println("No polling required!\n")

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
	recvDataTicker := time.NewTicker(receiveDataInterval)
	defer recvDataTicker.Stop()

	for {
		select {
		case <-recvDataTicker.C:
			// Check for incoming dispatches from SimConnect
			// Data is pushed automatically by SimConnect (event-driven!)
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
				fmt.Println("Connected to Flight Simulator.")

			case simconnect.RecvIDQuit:
				fmt.Println("Disconnected from Flight Simulator.")
				done <- true

			case simconnect.RecvIDException:
				recvException := *(*simconnect.RecvException)(ppData)
				fmt.Printf("SimConnect Exception: %d\n", recvException.Exception)

			case simconnect.RecvIDSimobjectData:
				// This is the event-driven data response from RequestDataOnSimObject
				data := *(*simconnect.RecvSimObjectData)(ppData)
				updateCounter++
				for _, simVar := range simVars {
					if simVar.DefineID == data.DefineID {
						val := *(*float64)(unsafe.Pointer(uintptr(ppData) + unsafe.Sizeof(data)))
						lastVal, ok := lastValues[simVar.DefineID]
						// Always show first value, then only on change
						if !ok || val != lastVal {
							timestamp := time.Now().Format("15:04:05.000")
							fmt.Printf("[%s] [#%d] %s: %.3f %s\n", timestamp, updateCounter, simVar.Name, val, simVar.Unit)
							lastValues[simVar.DefineID] = val
						}
						break
					}
				}

			case simconnect.RecvIDSimObjectDataByType:
				// Fallback handler in case any ByType responses come through
				data := *(*simconnect.RecvSimObjectDataByType)(ppData)
				updateCounter++
				for _, simVar := range simVars {
					if simVar.DefineID == data.DefineID {
						val := *(*float64)(unsafe.Pointer(uintptr(ppData) + unsafe.Sizeof(data)))
						lastVal, ok := lastValues[simVar.DefineID]
						if !ok || val != lastVal {
							timestamp := time.Now().Format("15:04:05.000")
							fmt.Printf("[%s] [#%d] %s: %.3f %s\n", timestamp, updateCounter, simVar.Name, val, simVar.Unit)
							lastValues[simVar.DefineID] = val
						}
						break
					}
				}
			}
		}
	}
}
