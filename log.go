package fs

import (
	"log"
)

// Logger provides the minimum interface for a logging client.
type Logger interface {
	Println(v ...interface{})
	Printf(format string, v ...interface{})
}

// DefaultLogger provides a default Logger implementation that uses Go's standard
// log.Println/Printf calls.
type DefaultLogger struct{}

func (DefaultLogger) Println(v ...interface{}) {
	log.Println(v...)
}

func (DefaultLogger) Printf(format string, v ...interface{}) {
	log.Printf(format, v...)
}
