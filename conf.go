package main

import (
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/y851592226/cate/encoding/json"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type Config struct {
	UserInfo   UserInfo   `json:"userInfo" yaml:"userInfo"`
	TargetInfo TargetInfo `json:"targetInfo" yaml:"targetInfo"`
	ConfigInfo ConfigInfo `json:"config" yaml:"config"`
	BAIConfig  BAIConfig  `json:"baiConfig" yaml:"baiConfig"`
}

type ConfigInfo struct {
	Debug      bool `json:"debug" yaml:"debug"`
	Special    bool `json:"special" yaml:"special"`
	UseFile    bool `json:"useFile" yaml:"useFile"`
	LogLevel   int  `json:"logLevel" yaml:"logLevel"`
	TryBest    bool `json:"tryBest" yaml:"tryBest"`
	Desc       bool `json:"desc" yaml:"desc"`
	ByKB       bool `json:"byKB" yaml:"byKB"`
	Concurrent bool `json:"concurrent" yaml:"concurrent"`
}

type BAIConfig struct {
	ClientID     string `json:"clientID" yaml:"clientID"`
	ClientSecret string `json:"clientSecret" yaml:"clientSecret"`
}

type UserInfo struct {
	Phone    string `json:"phone" yaml:"phone"`
	CardNo   string `json:"cardNo" yaml:"cardNo"`
	CardType string `json:"cardType" yaml:"cardType"`
	IsSelf   bool   `json:"isSelf" yaml:"isSelf"`
}

type TargetInfo struct {
	HosCode                string           `json:"hosCode" yaml:"hosCode"`
	FirstDeptCode          string           `json:"firstDeptCode" yaml:"firstDeptCode"`
	SecondDeptCode         string           `json:"secondDeptCode" yaml:"secondDeptCode"`
	Target                 string           `json:"target" yaml:"target"`
	DoctorNames            []string         `json:"doctorNames" yaml:"doctorNames"`
	DoctorTitleName        []string         `json:"doctorTitleName" yaml:"doctorTitleName"`
	Time                   string           `json:"time" yaml:"time"`
	DoctorNamesPattern     []*regexp.Regexp `json:"-" yaml:"-"`
	DoctorTitleNamePattern []*regexp.Regexp `json:"-" yaml:"-"`
	TargetDuration         int              `json:"targetDuration" yaml:"targetDuration"`
}

var conf = &Config{}
var once = sync.Once{}

func Conf() *Config {
	once.Do(func() {
		payload, err := os.ReadFile(Flags().confPath)
		if err != nil {
			logrus.Fatalf("read config file failed, err=%v", err.Error())
		}
		err = yaml.Unmarshal(payload, conf)
		if err != nil {
			logrus.Fatalf("unmarshal config file failed, err=%v", err.Error())
		}
		// fill in bai config
		payload, err = os.ReadFile("config/bai_config.yaml")
		if err == nil {
			err = yaml.Unmarshal(payload, &conf.BAIConfig)
			if err != nil {
				logrus.Warningf("unmarshal bai config file failed, err=%v", err.Error())
			}
		}
		// fill in doctor names
		if len(conf.TargetInfo.DoctorNames) == 0 && len(conf.TargetInfo.DoctorTitleName) == 0 {
			// no required
			conf.TargetInfo.DoctorNames = []string{""}
		}
		// preprocess doctor name pattern
		pattern := []*regexp.Regexp{}
		for _, d := range conf.TargetInfo.DoctorNames {
			pattern = append(pattern, regexp.MustCompile(d))
		}
		conf.TargetInfo.DoctorNamesPattern = pattern

		pattern = []*regexp.Regexp{}
		for _, d := range conf.TargetInfo.DoctorTitleName {
			pattern = append(pattern, regexp.MustCompile(d))
		}
		conf.TargetInfo.DoctorTitleNamePattern = pattern
		// fill in target
		if conf.TargetInfo.Target == "" {
			conf.TargetInfo.Target = time.Now().AddDate(0, 0, conf.TargetInfo.TargetDuration).Format("2006-01-02")
		}
		logrus.Infof("conf=%v", json.MarshalString(conf))
	})
	return conf
}
