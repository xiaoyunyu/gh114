package utils

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func Cause(err error) error {
	type causer interface {
		Cause() error
	}

	for err != nil {
		cause, ok := err.(causer)
		if !ok {
			break
		}
		err = cause.Cause()
	}
	return err
}

func IsTransientErr(err error) bool {
	if err == nil {
		return false
	}
	err = Cause(err)
	isTransient := isTransientNetworkErr(err) || matchTransientErrPattern(err)
	return isTransient
}

func matchTransientErrPattern(err error) bool {
	// TRANSIENT_ERROR_PATTERN allows to specify the pattern to match for errors that can be seen as transient
	// and retryable.
	pattern, _ := os.LookupEnv("TRANSIENT_ERROR_PATTERN")
	if pattern == "" {
		return false
	}
	match, _ := regexp.MatchString(pattern, generateErrorString(err))
	return match
}

func isTransientNetworkErr(err error) bool {
	switch err.(type) {
	case *net.DNSError, *net.OpError, net.UnknownNetworkError:
		return true
	}

	errorString := generateErrorString(err)
	if strings.Contains(errorString, "Connection closed by foreign host") {
		// For a URL error, where it replies back "connection closed"
		// retry again.
		return true
	} else if strings.Contains(errorString, "net/http: TLS handshake timeout") {
		// If error is - tlsHandshakeTimeoutError, retry.
		return true
	} else if strings.Contains(errorString, "i/o timeout") {
		// If error is - tcp timeoutError, retry.
		return true
	} else if strings.Contains(errorString, "connection timed out") {
		// If err is a net.Dial timeout, retry.
		return true
	} else if strings.Contains(errorString, "connection reset by peer") {
		// If err is a ECONNRESET, retry.
		return true
	} else if _, ok := err.(*url.Error); ok && strings.Contains(errorString, "EOF") {
		// If err is EOF, retry.
		return true
	}

	return false
}

func generateErrorString(err error) string {
	errorString := err.Error()
	if exitErr, ok := err.(*exec.ExitError); ok {
		errorString = fmt.Sprintf("%s %s", errorString, exitErr.Stderr)
	}
	return errorString
}
