package peerpicker

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func loopbackSetup() {
	ipWant := map[string]bool{
		"127.0.0.1": false,
		"127.0.0.2": false,
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		panic(err)
	}

	for _, addr := range addrs {
		ip := addr.(*net.IPNet).IP
		if ip.To4() == nil {
			continue
		}

		if _, ok := ipWant[ip.String()]; ok {
			ipWant[ip.String()] = true
		}
	}

	for k, v := range ipWant {
		if !v {
			panic(fmt.Sprintf("%s was not setup on the box, test cannot proceed", k))
		}
	}
}

func binaryCheck() {
	if _, err := os.Stat("app/app"); err != nil {
		panic("you must compile the app inside app/ to run this test")
	}
}

const (
	loop1 = "127.0.0.1"
	loop2 = "127.0.0.2"
)

func TestPeerPicker(t *testing.T) {
	loopbackSetup()
	binaryCheck()

	os.Remove(filepath.Join(os.TempDir(), loop1))
	os.Remove(filepath.Join(os.TempDir(), loop2))

	peer1, peer1Out, err := executeApp(loop1, loop2)
	if err != nil {
		panic(err)
	}
	peer2, peer2Out, err := executeApp(loop2, loop1)
	if err != nil {
		panic(err)
	}

	log.Println("all apps started")

	t.Log("wait for first peer to exit")

	_, err = peer1.Process.Wait()
	if err != nil {
		t.Errorf("TestPeerPicker: peer1 never saw peer2:\n%s", peer1Out.String())
	}

	t.Log("wait for second peer to exit")

	_, err = peer2.Process.Wait()
	if err != nil {
		t.Errorf("TestPeerPicker: peer2 never saw peer1:\n%s", peer2Out.String())
	}
}

func executeApp(iam, peer string) (*exec.Cmd, *bytes.Buffer, error) {
	cmd := exec.Command(
		"app/app",
		fmt.Sprintf("--ip=%s", iam),
		fmt.Sprintf("--peer=%s", peer),
		"--port=9999",
	)
	buff := &bytes.Buffer{}
	cmd.Stderr = buff
	cmd.Stdout = buff

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, buff, nil
}
