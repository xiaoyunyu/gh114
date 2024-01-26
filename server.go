package main

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/render"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/y851592226/cate/encoding/json"
)

const closeCodeServiceRestart = 1012 // See https://www.iana.org/assignments/websocket/websocket.xhtml

type service struct {
	r          *gin.Engine
	addr       string
	logger     *logrus.Entry
	target     string
	stopCh     chan struct{}
	msgCh      chan *httpMessage
	msgHandler sync.Map
}

type httpMessage struct {
	RequestUUID string         `json:"requestUUID"`
	URL         string         `json:"url"`
	Method      string         `json:"method"`
	Body        string         `json:"body"`
	ResponseCh  chan *response `json:"-"`
}

type response struct {
	RequestUUID string `json:"requestUUID"`
	Code        int    `json:"code"`
	Body        []byte `json:"body"`
	ContentType string `json:"contentType"`
}

func NewService(addr, target string) *service {
	r := gin.New()

	svc := &service{
		r:          r,
		addr:       addr,
		target:     target,
		logger:     logrus.WithField("component", "service"),
		stopCh:     make(chan struct{}),
		msgCh:      make(chan *httpMessage, 10),
		msgHandler: sync.Map{},
	}

	r.Any("/ws", svc.Websocket)
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, "OK")
	})
	r.NoRoute(svc.Proxy)
	return svc
}

func (s *service) Run() {
	go func() {
		err := s.r.Run(s.addr)
		if err != nil {
			logrus.Fatal(err.Error())
		}
	}()
	return
}

func (s *service) sendResponseToMsg(uuid string, err error, resp *response) {
	msgValue, ok := s.msgHandler.LoadAndDelete(uuid)
	if !ok {
		s.logger.Warningf("request uuid=%v not found in msg handler", uuid)
		return
	}
	msg := msgValue.(*httpMessage)
	defer func() {
		close(msg.ResponseCh)
	}()

	if err != nil {
		s.logger.Warningf("get response failed, err=%v", err.Error())
		msg.ResponseCh <- &response{
			Code:        500,
			ContentType: "text/plain; charset=utf-8",
			Body:        []byte(err.Error()),
		}
		return
	}
	msg.ResponseCh <- resp
}

func (s *service) Websocket(c *gin.Context) {
	s.logger.Debug("Handle entered")
	r := c.Request

	w := c.Writer
	var upgrader websocket.Upgrader
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Errorf("error upgrading websocket, err=%v", err.Error())
		return
	}

	handlerCh := make(chan struct{})

	go func() {
		select {
		case <-s.stopCh:
			// Send a close message to tell the client to immediately reconnect
			s.logger.Debug("Sending close message to client")
			werr := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(closeCodeServiceRestart, "Restarting"))
			if werr != nil {
				s.logger.Warnf("Failed to send close message to client, err=%v", werr.Error())
			}
			_ = conn.Close()

		case <-handlerCh:
			s.logger.Debug("Handler exit complete")
		}
	}()
	// retry remaining http msg
	remained := []*httpMessage{}
	s.msgHandler.Range(func(key, value any) bool {
		remained = append(remained, value.(*httpMessage))
		return true
	})
	go func() {
		for _, msg := range remained {
			s.msgCh <- msg
		}
	}()

	s.logger.Debug("Connection upgraded to WebSocket. Entering send loop.")
	go func() {
		for {
			select {
			case <-s.stopCh:
				return

			case <-handlerCh:
				return

			case msg := <-s.msgCh:
				s.msgHandler.Store(msg.RequestUUID, msg)
				payload, jerr := json.Marshal(msg)
				if jerr != nil {
					s.sendResponseToMsg(msg.RequestUUID, jerr, nil)
					break
				}
				werr := conn.WriteMessage(websocket.TextMessage, payload)
				if werr != nil {
					if websocket.IsCloseError(werr, websocket.CloseAbnormalClosure) {
						s.logger.Debug("Handler disconnected")
					} else {
						s.logger.Errorf("Handler exiting on error: %#v", werr)
					}
					close(handlerCh)
					return
				}
			}
		}
	}()

	for {
		messageType, msg, err := conn.ReadMessage()
		if err != nil {
			// We close abnormally, because we're just closing the connection in the client,
			// which is okay. There's no value delaying closure of the connection unnecessarily.
			if websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
				s.logger.Debug("Handler disconnected")
			} else {
				s.logger.Errorf("Handler exiting on error: %#v", err)
			}
			close(handlerCh)
			return
		}

		switch messageType {
		case websocket.BinaryMessage:
			resp := &response{}
			jerr := json.Unmarshal(msg, &resp)
			if jerr != nil {
				jerr = errors.Wrap(jerr, "Failed to unmarshal the object")
			}
			s.sendResponseToMsg(resp.RequestUUID, jerr, resp)

		default:
			s.logger.Error("Dropping unknown message type.")
			continue
		}
	}
}

func (s *service) Proxy(c *gin.Context) {
	u := *c.Request.URL
	u.Scheme = "https"
	u.Host = s.target

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, fmt.Sprintf("bad request, err=%v", err))
		return
	}
	msg := &httpMessage{
		RequestUUID: uuid.New().String(),
		URL:         u.String(),
		Method:      c.Request.Method,
		Body:        string(body),
		ResponseCh:  make(chan *response, 1),
	}
	s.msgCh <- msg

	select {
	case resp := <-msg.ResponseCh:
		c.Render(resp.Code, render.Data{
			ContentType: resp.ContentType,
			Data:        []byte(resp.Body),
		})

	case <-c.Done():
		c.JSON(500, c.Err())
		return
	}
}
