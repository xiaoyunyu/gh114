package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/xiaoyunyu/gh114/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestGetImageCode(t *testing.T) {
	cli, err := NewClient()
	assert.Nil(t, err)
	_, code, err := cli.getImageCode(context.Background(), &getImageRequest{
		Time:      time.Now().UnixMilli(),
		CheckType: "LOGIN",
	})
	assert.Nilf(t, err, "err=%v", err)
	if err != nil {
		return
	}
	logrus.Infof("code=%v", code)
}

func TestGetOCRResult(t *testing.T) {
	logrus.StandardLogger().Level = logrus.DebugLevel
	cli, err := NewClient()
	assert.Nil(t, err)
	im, _ := os.ReadFile("code.png")
	code, err := cli.getOCRResult(context.Background(), im)
	assert.Nilf(t, err, "err=%v", err)
	if err != nil {
		return
	}
	logrus.Infof("code=%v", code)
}

func TestGetMsg(t *testing.T) {
	re := regexp.MustCompile(`北京114.*短信验证码为【(\d+)`)
	logrus.StandardLogger().Level = logrus.DebugLevel
	g := utils.NewIMessageGetter()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := g.GetCode(ctx, re, time.Unix(1699535011-1, 0), false)
	fmt.Println(res, err)
}
