package main

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestLogin(t *testing.T) {
	cli, err := NewClient()
	assert.Nil(t, err)
	ctx, err := login(cli)
	assert.Nil(t, err)
	c := getCookies(ctx)
	logrus.Infof("cookies=%v", c)
}

func TestEncrypt(t *testing.T) {
	cli, err := NewClient()
	if err != nil {
		logrus.Fatalf("new client failed, err=%v", err)
	}

	//patch := gomonkey.ApplyMethodFunc(&big.Int{}, "SetBytes", func(buf []byte) *big.Int {
	//	logrus.Infof("buf=%v", buf)
	//	return &big.Int{}
	//})
	//defer patch.Reset()

	for i := 0; i < 2; i++ {
		res, err := cli.encrypt("13200000")
		if err != nil {
			logrus.Fatalf(err.Error())
		}
		fmt.Printf("url escaped=%v     ", url.QueryEscape(res))
	}
}
