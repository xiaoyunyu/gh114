package utils

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type writer struct {
	gin.ResponseWriter
	relayWriter io.Writer
}

func (w *writer) Write(body []byte) (int, error) {
	return w.relayWriter.Write(body)
}

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// replace response writer
		var log bytes.Buffer
		rsp := io.MultiWriter(c.Writer, &log)
		c.Writer = &writer{
			ResponseWriter: c.Writer,
			relayWriter:    rsp,
		}
		// read request
		reqBody, _ := io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewReader(reqBody))
		// pre log
		method := c.Request.Method
		path := c.Request.URL.Path
		clientIP := c.ClientIP()
		clientUserAgent := c.Request.UserAgent()
		headers := c.Request.Header
		params := c.Params

		requestEntry := fmt.Sprintf("clientIp=%s, method=%s, path=%s, params=%s, headers=%s, userAgent=%s, body=%v",
			clientIP, method, path, params, headers, clientUserAgent, string(reqBody))

		if len(c.Errors) > 0 {
			errMsg := fmt.Sprintf("%s, %s", c.Errors.ByType(gin.ErrorTypePrivate).String(), requestEntry)
			logrus.WithFields(logrus.Fields{"opType": "router using logger"}).Error(errMsg)
		} else {
			infoMsg := fmt.Sprintf("[GIN] Before Request, %s", requestEntry)
			logrus.WithFields(logrus.Fields{"opType": "router using logger"}).Info(infoMsg)
		}

		// process request
		start := time.Now()
		c.Next()
		stop := time.Now()

		// post log
		latency := stop.Sub(start)
		prefix := ""
		if latency > 5*time.Second {
			prefix = "[attention] latency is greater than 5s. "
		}
		statusCode := c.Writer.Status()

		postEntry := fmt.Sprintf("clientIP=%s, method=%s, path=%s, statusCode=%v, latency=%s, respBody=%v",
			clientIP, method, path, statusCode, latency, log.String())

		if len(c.Errors) > 0 {
			errMsg := fmt.Sprintf("[GIN]%v After Request, %s, err=%s", prefix, postEntry, c.Errors.ByType(gin.ErrorTypePrivate))
			logrus.WithFields(logrus.Fields{"opType": "router using logger"}).Error(errMsg)
		} else {
			infoMsg := fmt.Sprintf("[GIN]%v After Request, %s", prefix, postEntry)
			logrus.WithFields(logrus.Fields{"opType": "router using logger"}).Info(infoMsg)
		}
	}
}
