package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
    "regexp"

	"gopkg.in/yaml.v2"
	"nhooyr.io/websocket"
)

var conf = make(map[string]string)
var buffer = make(chan []byte, 2000)
var termination = make(chan string, 10)
var configfile = "gls_client.yaml"
var timeout = 5000
var shortsleeploop = 500

func getConfig(filename string) {

	log.Print("config: ", filename)

	yconfigfile, err := os.ReadFile(filename)
	if err != nil {
		log.Fatal(err)
	}

	err2 := yaml.Unmarshal(yconfigfile, &conf)
	if err2 != nil {
		log.Fatal(err2)
	}

	for k, v := range conf {
		if k == "flags" {
			flags := strings.Fields(v)
			for flag := range flags {
				conf["flag."+flags[flag]] = "true"
			}
			delete(conf, "flags")
		}
	}

	for k, v := range conf {
		log.Printf("    %s -> %s\n", k, v)
	}

}

var sessionid string = ""

func generateSessionID() {
	if sessionid == "" {
		// create a sessionid for this session
		currentTime := time.Now()
		sessionid = fmt.Sprintf(
			"%s-%s-%s-%d",
			currentTime.Format("20060102-150405"),
			os.Getenv("USERNAME"),
			os.Getenv("COMPUTERNAME"),
			os.Getpid(),
		)
	}
}

func tailToServer(logfilename string, server string) {

	// create a timeout context
	ctx, reclaim_context_resources := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer reclaim_context_resources()

	// connect to server and send session id
	c, _, err := websocket.Dial(ctx, server, nil)
	if err != nil {
		log.Println(err)
        match, _ := regexp.MatchString("connection refused", fmt.Sprint(err))
        if match {
			time.Sleep(5 * time.Duration(timeout) * time.Millisecond)
        }
		return
	}
    defer c.Close(websocket.StatusNormalClosure, "closing connection")

	// handshake by sending session ID and receiving remote
	// logfile length (for new file this will be "0")
	log.Printf("sending session id %s to server %s\n", sessionid, server)
	err6 := c.Write(ctx, websocket.MessageText, []byte(sessionid))
    if err6 != nil {
		log.Println(err,"Write() failed, disconecting")
	    c.Close(websocket.StatusInternalError, "websocket Read() failed")
    }
	mtype, str, err2 := c.Read(ctx)
	if err2 != nil {
		log.Println(err,"Read() failed, disconecting")
	    c.Close(websocket.StatusInternalError, "websocket Read() failed")
		return
	}
	if mtype != websocket.MessageText {
		log.Println("remote file size handshake not in text format")
		return
	}
	remoteSize, err3 := strconv.ParseInt(string(str), 10, 64)
	if err3 != nil {
		log.Println("remote provided file size not an integer: ", str, ":", err)
		return
	}
	log.Printf("received file length %d, handshake complete", remoteSize)

	// Open the local log file
	loopcount := 0
	var f *os.File
	for {
		loopcount++
		f, err = os.Open(logfilename)
		if err != nil {
			if loopcount == 1 {
				log.Printf("log file does not yet exist - waiting")
			}
			if len(termination) > 0 {
				// when termination requested
		        log.Println(err,"normal ending while waiting for log creation")
				return
			}
			time.Sleep(time.Duration(shortsleeploop) * time.Millisecond)
		} else {
			break
		}
	}
	log.Printf("log file open for reading - %s", logfilename)
	defer f.Close()

	// tail from where server log file ends
	// if local file smaller tail from local end
	endptr, err4 := f.Seek(0, 2)
	if err4 != nil {
		log.Println(err, ": disconnecting 1")
		return
	}
	if endptr > remoteSize {
		_, err5 := f.Seek(remoteSize, 0)
		if err5 != nil {
			log.Println(err, ": disconnecting 2")
			return
		}
	}

	// watch logfile and send data to server
	for {

		// read data from log file
		databytes := make([]byte, 2000)
		n, err := f.Read(databytes)
		if n == 0 {
			// pointer is at end of file
			if len(termination) > 0 {
				// when termination requested
		        log.Println(err,"normal ending while tailing")
				return
			}
			time.Sleep(time.Duration(shortsleeploop) * time.Millisecond)
			continue
		} else if err != nil {
			log.Println(err, ": disconnecting 3")
			return
		}

		// write data to remote server
		err = c.Write(ctx, websocket.MessageBinary, databytes)
		if err != nil {
			log.Println(err)
			return
		}

	}

}

func main() {

	generateSessionID()
	getConfig(configfile)

	// wipe logfile on startup if requested
	if conf["flag.removelogfile"] == "true" {
		log.Printf("removing log file")
		os.Remove(conf["logfile"])
	}
	t, _ := strconv.Atoi(conf["timeout"])
	if t > 0 {
		timeout = t
	}

    // tail file to remote server (in background)
	go func() {
		for {
			tailToServer(conf["logfile"], conf["server"])
			if len(termination) > 0 {
				<-termination
				break
			}
			time.Sleep(time.Duration(timeout) * time.Millisecond)
		}
	}()

	// execute command (in foreground)
    cmd := strings.Fields(conf["command"])
	log.Printf("running command: %#v", cmd)
	c := exec.Command(cmd[0],cmd[1:]...)
	err := c.Run()
    if err != nil {
	    log.Println("Run() complete:",err)
    } else {
	    log.Println("Run() complete: normal exit")
    }

    // send termination message and wait for response
    // or timeout
	termination <- "log file session ended"
	countdown := timeout
	for len(termination) > 0 && countdown > 0 {
		countdown -= shortsleeploop
		time.Sleep(time.Duration(shortsleeploop) * time.Millisecond)
	}
    log.Println("sender finished , exiting")

}

/*
	TODO:
	- review to avoid crash - as crash takes out the exec.Command()
    - FIX when waiting for Read or file to appear connection can go away and it is not known
	- graceful closure

*/
