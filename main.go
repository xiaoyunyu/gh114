package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/xiaoyunyu/gh114/utils"
	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/y851592226/cate/encoding/json"
	"github.com/y851592226/cate/httpreq"
	"go.uber.org/atomic"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	host = "www.114yygh.com"
	svc  = "127.0.0.1:9001"
)

var loginExpiredTime *time.Time

func init() {
	runtime.LockOSThread()
}

func main() {
	initLog()
	s := NewService(svc, host)
	s.Run()
	// wait for svc start up
	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 10*time.Second, true,
		func(ctx context.Context) (done bool, err error) {
			resp, err := httpreq.DefaultClient.Get(fmt.Sprintf("http://%v/healthz", svc))
			if err == nil && resp.StatusCode() == 200 {
				return true, nil
			}
			return false, nil
		})
	if err != nil {
		logrus.Fatalf(err.Error())
	}

	main1()
}

func main1() {
	initLog()
	run()
}

func initLog() {
	logrus.SetFormatter(&nested.Formatter{
		TimestampFormat: time.RFC3339,
	})
	logLevel := logrus.InfoLevel
	if Conf().ConfigInfo.LogLevel != 0 {
		logLevel = logrus.Level(Conf().ConfigInfo.LogLevel)
	}
	logrus.SetLevel(logLevel)

	if logLevel > logrus.InfoLevel {
		logrus.SetReportCaller(true)
	}
}

func run() {
	defer time.Sleep(1 * time.Second)
	cli, err := NewClient(svc)
	if err != nil {
		logrus.Fatalf("new client failed, err=%v", err)
	}
	var ctx context.Context
	for {
		ctx, err = login(cli)
		if err != nil {
			if utils.IsTransientErr(err) {
				continue
			}
			logrus.Errorf("login failed, err=%v", err)
			return
		}
		break
	}
	for {
		err = loop(ctx, cli)
		if err != nil {
			if utils.IsTransientErr(err) {
				continue
			}
			logrus.Errorf("loop failed, err=%v", err)
			break
		}
		break
	}
}

func loop(ctx context.Context, cli *client) error {
	patientID := PatientID{
		Card: Card{
			CardNo:   Conf().UserInfo.CardNo,
			CardType: Conf().UserInfo.CardType,
		},
	}

	tm := time.Now()
	if Conf().TargetInfo.Time != "" {
		fullTime := Conf().TargetInfo.Time
		if fullTime[0] == 'T' {
			fullTime = tm.Format("2006-01-02") + fullTime
		}
		start, err := time.Parse(time.RFC3339, fullTime)
		if err != nil {
			return errors.Wrap(err, "parse time failed")
		}
		// 提前 10s 启动
		start = start.Add(-10 * time.Second)

		resTm := start.Sub(tm)
		expiredDuration, err := getLoginExpiredDuration()
		if err != nil {
			return err
		}
		if expiredDuration < resTm {
			// we should get cookie again
			sleepTm := resTm - 10*time.Minute
			logrus.Infof("login token will be expired in %v, we will sleep %v then you should login again", expiredDuration, sleepTm)
			time.Sleep(sleepTm)
			ctx, err = login(cli)
			if err != nil {
				return err
			}
			resTm = start.Sub(time.Now())
		}
		if resTm > 0 {
			logrus.Infof("we need to sleep %v to %v", resTm, start)
			time.Sleep(resTm)
		}
	}

	encryptedMobile := getEncryptedMobile(ctx)

	tm = time.Now()
	defer func(start time.Time) {
		logrus.Infof("total cost=%v", time.Now().Sub(tm))
	}(tm)

	logrus.Infof("starting to loop, curr time=%v, target date=%v", tm, Conf().TargetInfo.Target)
	target := targetDeptTime{
		targetDept: targetDept{
			FirstDeptCode:  Conf().TargetInfo.FirstDeptCode,
			SecondDeptCode: Conf().TargetInfo.SecondDeptCode,
			HosCode:        Conf().TargetInfo.HosCode,
		},
		Target: Conf().TargetInfo.Target,
	}
	// step 1) get detail info
	detailCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var details []*detailInfo
	detailCh := make(chan []*detailInfo, 1)
	concurrency := make(chan struct{}, 4)
	go func() {
		for {
			if details != nil {
				return
			}
			if detailCtx.Err() != nil {
				return
			}
			select {
			case concurrency <- struct{}{}:
			}
			go func() {
				defer func() { <-concurrency }()
				d, residual, getDetailErr := cli.getDetail(ctx, tm, &target)
				if getDetailErr != nil {
					logrus.Warningf("get detail failed, err=%v", getDetailErr)
					return
				}
				if residual {
					detailCh <- d
				}
			}()
			time.Sleep(50 * time.Millisecond)
		}
	}()

	select {
	case details = <-detailCh:
		break
	case <-detailCtx.Done():
		return detailCtx.Err()
	}

	cancel()
	logrus.Debugf("cost %v at getting detials", time.Now().Sub(tm))

	// step 2) start worker to get order
	workerNum := 1
	if Conf().ConfigInfo.Concurrent && len(Conf().TargetInfo.DoctorNames) > 0 {
		workerNum = len(Conf().TargetInfo.DoctorNames)
		if Conf().TargetInfo.DoctorNames[0] == "" && workerNum == 1 {
			workerNum = 3 // default concurrency
		}
	}
	candidateCh := make(chan *detail, workerNum)
	successCh := make(chan struct{}, workerNum)
	orderFailedNum := atomic.NewInt32(0)
	allOrderFailedCh := make(chan struct{}, workerNum)
	orderCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	for i := 0; i < workerNum; i++ {
		go func() {
			for candidate := range candidateCh {
				if orderCtx.Err() != nil {
					continue
				}
				if len(successCh) > 0 {
					// already got order
					return
				}
				getOrderErr := getOrder(orderCtx, cli, tm, candidate, target, encryptedMobile, patientID)
				if getOrderErr != nil {
					if int(orderFailedNum.Add(-1)) == 0 {
						allOrderFailedCh <- struct{}{}
					}
					continue
				}
				successCh <- struct{}{}
				return
			}
		}()
	}
	// step 3) get candidates
	candidates := []*detail{}
	targetDoctors := Conf().TargetInfo.DoctorNamesPattern
	targetDoctorTitle := Conf().TargetInfo.DoctorTitleNamePattern
	i := 0
	if Conf().ConfigInfo.Desc {
		i = len(details) - 1
	}
	for i >= 0 && i < len(details) {
		subDetails := details[i]
		if Conf().ConfigInfo.Desc {
			i--
		} else {
			i++
		}
		j := 0
		if Conf().ConfigInfo.Desc {
			j = len(subDetails.Detail) - 1
		}
		for j >= 0 && j < len(subDetails.Detail) {
			candidate := subDetails.Detail[j]
			if Conf().ConfigInfo.Desc {
				j--
			} else {
				j++
			}
			if candidate.isSpecial() && !Conf().ConfigInfo.Special {
				continue
			}
			for _, t := range targetDoctors {
				if !t.MatchString(candidate.DoctorName) {
					continue
				}
				logrus.Debugf("sending to candidate ch")
				candidates = append(candidates, candidate)
			}
			for _, t := range targetDoctorTitle {
				if !t.MatchString(candidate.DoctorTitleName) {
					continue
				}
				logrus.Debugf("sending to candidate ch")
				candidates = append(candidates, candidate)
			}
		}
	}
	defer func() {
		close(candidateCh)
	}()

marker:
	if len(candidates) == 0 {
		return fmt.Errorf("not found any available doctor")
	}
	orderFailedNum.Add(int32(len(candidates)))
	for _, c := range candidates {
		candidateCh <- c
	}

	// step 4) collect result
	select {
	case <-successCh:
		return nil

	case <-allOrderFailedCh:
		err := errors.Errorf("all wanted doctors=%#v tried failed", Conf().TargetInfo.DoctorNames)
		if Conf().ConfigInfo.TryBest {
			logrus.Debugf("goto marker")
			time.Sleep(50 * time.Millisecond)
			goto marker
		}
		return err

	case <-orderCtx.Done():
		return orderCtx.Err()
	}
}

func getOrder(ctx context.Context, cli *client, tm time.Time, doctorDetail *detail, target targetDeptTime,
	encryptedMobile string, patientID PatientID) error {

	logrus.Infof("found doctor %v, title=%v", doctorDetail.DoctorName, doctorDetail.DoctorTitleName)
	zeroCode := extraLastCode(doctorDetail.FCode)
	dutyTime := ""
	uniqKey := doctorDetail.UniqProductKey
	for _, p := range doctorDetail.Period {
		if !haveNumber(p.NCode, zeroCode) {
			continue
		}
		dutyTime = p.DutyTime
		uniqKey = p.UniqProductKey
		break
	}
	td := targetDoctor{
		targetDeptTime: target,
		UniqProductKey: uniqKey,
		DutyTime:       dutyTime,
	}
	confirmRes, err := cli.confirm(ctx, tm, &td)
	if err != nil {
		logrus.Warnf("confirm failed, err=%v", err)
		return err
	}
	var code string
	if confirmRes.DataItem.SMSCode > 2 {
		code = cli.getSMSCodeByCache(ctx, &getSMSCodeRequest{
			Time:           tm.UnixMilli(),
			Mobile:         encryptedMobile,
			SMSKey:         "ORDER_CODE",
			UniqProductKey: doctorDetail.UniqProductKey,
			DoctorName:     doctorDetail.DoctorName,
		})
	}
	//err = cli.check(ctx, tm, &patientID)
	//if err != nil {
	//	return errors.Wrap(err, "check patient failed")
	//}
	hosCardId := ""
	if confirmRes.NeedRemoteHospitalCard {
		hosCardId = strconv.Itoa(confirmRes.DataItem.HospitalCardId)
	}

	if Conf().ConfigInfo.Debug {
		logrus.Infof("successfully get order, orderInfo=%#v", "debug")
		return nil
	}

	orderInfo, err := cli.save(ctx, tm, &saveRequest{
		targetDoctor:   td,
		PatientID:      patientID,
		SMSCode:        code,
		HospitalCardId: hosCardId,
		Phone:          Conf().UserInfo.Phone,
		ConfirmToken:   confirmRes.ConfirmToken,
		IsSelf:         Conf().UserInfo.IsSelf,
	})
	if err != nil {
		logrus.Warnf("save order failed, err=%v", err)
		return err
	}
	logrus.Infof("successfully get order, orderInfo=%#v", orderInfo)
	return nil
}

func login(cli *client) (context.Context, error) {
	//ctx, err := loginByHistory()
	//if err == nil {
	//	if getCookieExpiredTime(ctx) > 0 {
	//		return ctx, err
	//	}
	//}
	//
	//if Conf().ConfigInfo.UseFile {
	//	return loginWithFile()
	//}
	return loginWithNormal(cli)
}

func loginWithFile() (context.Context, error) {
	ctx := context.Background()
	cookiesVal, err := readFile("./data/cookie")
	if err != nil {
		return nil, err
	}
	dump := http.Request{Header: map[string][]string{"Cookie": strings.Split(cookiesVal, "; ")}}
	ctx = addCookies(ctx, dump.Cookies())

	mobileVal, err := readFile("./data/mobile")
	if err != nil {
		return nil, err
	}
	ctx = setEncryptedMobile(ctx, mobileVal)
	return ctx, nil
}

func loginByHistory() (context.Context, error) {
	ctx := context.Background()
	cookiesVal, err := readFile("./data/cookie.json")
	if err != nil {
		return nil, err
	}
	cookies := []*http.Cookie{}
	err = json.Unmarshal([]byte(cookiesVal), &cookies)
	if err != nil {
		return nil, err
	}
	ctx = addCookies(ctx, cookies)

	mobileVal, err := readFile("./data/mobile")
	if err != nil {
		return nil, err
	}
	ctx = setEncryptedMobile(ctx, mobileVal)
	return ctx, nil
}

func loginWithNormal(cli *client) (context.Context, error) {
	var err error
	var imageCode string
	// init ctx
	logrus.Infof("starting login")
	tm := time.Now()
	tmms := tm.UnixMilli()
	ctx := context.Background()
marker:
	ctx, imageCode, err = cli.getImageCode(ctx, &getImageRequest{
		Time:      tmms,
		CheckType: "LOGIN",
	})
	if err != nil {
		return nil, errors.Wrap(err, "get image code failed")
	}
	ctx, err = cli.checkCode(ctx, &checkCodeRequest{
		Time:      tmms,
		Code:      imageCode,
		Mobile:    Conf().UserInfo.Phone,
		CheckType: "LOGIN",
	})
	if err != nil {
		return nil, errors.Wrap(err, "check code failed")
	}
	encryptedMobile, err := cli.encrypt(Conf().UserInfo.Phone)
	if err != nil {
		return nil, errors.Wrap(err, "encrypt mobile failed")
	}
	ctx = setEncryptedMobile(ctx, encryptedMobile)

	code, err := cli.getSMSCode(ctx, &getSMSCodeForLoginRequest{
		Time:   tmms,
		Mobile: encryptedMobile,
		SMSKey: "LOGIN",
		Code:   imageCode,
	})
	if err != nil {
		if strings.Contains(err.Error(), "图形验证码输入错误") {
			goto marker
		}
		return nil, err
	}
	encryptedCode, err := cli.encrypt(code)
	if err != nil {
		return nil, errors.Wrap(err, "encrypt code failed")
	}

	ctx, err = cli.login(ctx, tm, &loginRequest{
		Mobile: encryptedMobile,
		Code:   encryptedCode,
	})
	if err != nil {
		return nil, errors.Wrap(err, "login failed")
	}
	logrus.Infof("finish login")

	mobile := getEncryptedMobile(ctx)
	err = os.WriteFile("./data/mobile", []byte(mobile), 0777)
	if err != nil {
		logrus.Warningf("save mobile failed, err=%v", err.Error())
	}
	setLoginTime()
	return ctx, nil
}

func setLoginTime() {
	now := time.Now()
	loginExpiredTime = &now
}

func getLoginExpiredDuration() (time.Duration, error) {
	if loginExpiredTime == nil {
		return 0, fmt.Errorf("loginExpiredTime not set")
	}

	// we think login will be expired in 30 min
	return 30*time.Minute - time.Now().Sub(*loginExpiredTime), nil
}
