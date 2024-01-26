package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xiaoyunyu/gh114/utils"
	"github.com/buger/jsonparser"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/y851592226/cate/encoding/json"
	"github.com/y851592226/cate/httpreq"
	"github.com/y851592226/cate/httpreq/binding"
)

const (
	cookieKey          = "cookie"
	encryptedMobileKey = "mobile"
)

var specialKeys = []string{"特需", "国际"}

type client struct {
	cli           *httpreq.Client
	commonHeaders map[string][]string
	host          string
	publicKey     *rsa.PublicKey
	random        []byte
	msgGetter     *utils.IMessageGetter
	msgPattern    *regexp.Regexp

	cache    *sync.Map
	cacheFlg *sync.Map
}

func NewClient(host string) (*client, error) {
	httpCli, _ := httpreq.NewClient(
		//httpreq.SetClientDebug(false, false),
		httpreq.AddClientRequestOptionFunc(httpreq.SetRequestRetryTimes(3)))
	headers, err := getHeaders()
	if err != nil {
		return nil, err
	}
	pk, err := getPublicKey()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(host, "http") {
		host = "http://" + host
	}
	msgGetter, err := utils.NewIMessageGetter()
	if err != nil {
		return nil, err
	}
	cli := &client{cli: httpCli,
		commonHeaders: headers,
		host:          host,
		publicKey:     pk,
		random:        makeRandom(),
		msgGetter:     msgGetter,
		msgPattern:    regexp.MustCompile(`北京114.*短信验证码为【(\d+)`),
		cache:         &sync.Map{},
		cacheFlg:      &sync.Map{},
	}

	return cli, nil
}

func makeRandom() []byte {
	tab := make([]int, 0, 256)
	for i := 0; i < 256; i++ {
		tab = append(tab, i)
	}
	res := make([]byte, 0, 256)
	i := 0
	j := 0
	for len(res) < 256 {
		i = (i + 1) % 256
		j = (j + tab[i]) % 256
		tab[i], tab[j] = tab[j], tab[i]
		tmp := tab[(tab[i]+tab[j])%256]
		if tmp == 0 {
			continue
		}
		res = append(res, byte(tmp))
	}
	return res
}

func readFile(path string) (string, error) {
	val, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	res := string(val)
	if res == "" {
		return "", fmt.Errorf("empty path=%v", path)
	}
	return res, nil
}

func getPublicKey() (*rsa.PublicKey, error) {
	payload, err := readFile("data/public_key")
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(payload)) //将密钥解析成公钥实例
	if block == nil {
		return nil, errors.New("public key error")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "ParsePKIXPublicKey failed")
	}
	pk, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.Errorf("invalid rsa public key")
	}
	// todo: why?
	pk.E = 65537
	return pk, nil
}

func getHeaders() (map[string][]string, error) {
	pl, err := readFile("data/headers")
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile("'")
	if err != nil {
		return nil, err
	}
	loc := re.FindAllStringIndex(pl, -1)
	headers := map[string][]string{}
	for i := 0; i < len(loc); i += 2 {
		start := loc[i][0] + 1
		end := loc[i+1][0]
		hd := pl[start:end]
		fds := strings.SplitN(hd, ":", 2)
		if len(fds) != 2 {
			return nil, fmt.Errorf("invalid header=%v", hd)
		}
		headers[strings.TrimSpace(fds[0])] = []string{strings.TrimSpace(fds[1])}
	}
	return headers, nil
}

type getImageRequest struct {
	Time      int64  `json:"_time" form:"_time"`
	CheckType string `json:"checkType" form:"checkType"`
}

func (c *client) getImageCode(ctx context.Context, req *getImageRequest) (context.Context, string, error) {
	u := fmt.Sprintf("%s/mobile/img/getImgCode", c.host)
	resp, err := c.cli.Get(u,
		httpreq.SetRequestDebug(true, false),
		c.addRequestMetaInfo(ctx),
		httpreq.SetRequestQuery(req))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode() != 200 {
		return nil, "", errors.Errorf("status code=%v", resp.StatusCode())
	}
	code, err := c.getOCRResult(ctx, resp.Body())
	if err != nil {
		return nil, "", err
	}
	code = extractCode(code)
	logrus.Debugf("code is %v", code)
	return ctx, code, nil
}

type ocrResult struct {
	WordsResult []*WordResult `json:"words_result"`
	ErrMsg      string        `json:"error_msg"`
}

type WordResult struct {
	Word string `json:"words"`
}

func (c *client) getAccessToken(ak, sk string) (string, error) {
	u := "https://aip.baidubce.com/oauth/2.0/token"
	resp, err := c.cli.Post(u, httpreq.SetRequestDebug(false, false), httpreq.SetRequestPostForm(map[string]interface{}{
		"grant_type":    "client_credentials",
		"client_id":     ak,
		"client_secret": sk,
	}))
	if err != nil {
		return "", err
	}

	accessTokenObj := map[string]interface{}{}
	logrus.Debugf("resp=%v", string(resp.Body()))
	err = json.Unmarshal(resp.Body(), &accessTokenObj)
	if err != nil {
		return "", err
	}
	return accessTokenObj["access_token"].(string), nil
}

func (c *client) getOCRResult(ctx context.Context, imageStr []byte) (string, error) {
	wd, _ := os.Getwd()
	fmt.Println("保存图片验证码到 code.jpg...")
	_ = os.WriteFile(filepath.Join(wd, "code.jpg"), imageStr, os.ModePerm)
	if Conf().BAIConfig.ClientID == "" {
		// need input img code manually
		fmt.Println("百度 AI 配置为空, 无法通过 AI 识别图片验证码, 请查看图片 code.jpg 并手动输入验证码...")
		fmt.Printf("==>请输入图片验证码: ")
		code := ""
		_, err := fmt.Scanf("%v", &code)
		if err != nil {
			return "", err
		}
		return code, nil
	}

	at, err := c.getAccessToken(Conf().BAIConfig.ClientID, Conf().BAIConfig.ClientSecret)
	if err != nil {
		return "", errors.Wrap(err, "get access token failed")
	}
	resp, err := c.cli.Post("https://aip.baidubce.com/rest/2.0/ocr/v1/numbers", httpreq.SetRequestDebug(true, true),
		httpreq.SetRequestQuery(map[string]interface{}{
			"access_token": at,
		}),
		httpreq.SetRequestFormValues(url.Values{"image": []string{base64.StdEncoding.EncodeToString(imageStr)}}),
	)
	if err != nil {
		return "", err
	}
	if resp.StatusCode() != 200 {
		return "", errors.Errorf("getOCRResult failed, status=%v", resp.Status())
	}
	r := &ocrResult{}
	err = resp.BindJSON(r)
	if err != nil {
		return "", err
	}
	if r.ErrMsg != "" {
		return "", errors.Errorf("getOCRResult failed, msg=%v", r.ErrMsg)
	}
	if len(r.WordsResult) == 0 {
		return "", errors.New("getOCRResult return empty")
	}
	return r.WordsResult[0].Word, nil
}

func extractCode(s string) string {
	res := ""
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			res += string(ch)
		}
	}
	return res
}

type checkCodeRequest struct {
	Time      int64  `json:"_time"`
	Code      string `json:"code"`
	Mobile    string `json:"mobile"` // plain text
	CheckType string `json:"checkType"`
}

//
//func (c *client) checkCode(ctx context.Context, req *checkCodeRequest) (context.Context, error) {
//	u := fmt.Sprintf("%s/web/checkcode", c.host)
//	_, err := c.cli.Get(u,
//		c.addRequestMetaInfo(ctx),
//		httpreq.SetRequestQuery(req),
//		httpreq.AddRequestMiddlewares(checkResponseErrorMiddleware),
//	)
//	if err != nil {
//		return nil, err
//	}
//	return ctx, nil
//}

type loginRequest struct {
	Mobile string `json:"mobile"`
	Code   string `json:"code"`
}

type loginData struct {
	AuthStatus string `json:"authStatus"` // need be AUTH_PASS
	UserID     string `json:"userId"`
}

func (c *client) login(ctx context.Context, tm time.Time, req *loginRequest) (context.Context, error) {
	u := fmt.Sprintf("%s/mobile/login", c.host)
	respData := &loginData{}
	_, err := c.cli.Post(u,
		c.addRequestMetaInfo(ctx),
		httpreq.SetRequestQueryValues(map[string][]string{"_time": {strconv.FormatInt(tm.UnixMilli(), 10)}}),
		httpreq.SetRequestBody(req),
		httpreq.AddRequestMiddlewares(addBindResponseMiddleware(&respData), checkResponseErrorMiddleware),
	)
	if err != nil {
		return nil, err
	}

	if respData.AuthStatus != "AUTH_PASS" {
		return nil, errors.Errorf("auth status=%v", respData.AuthStatus)
	}

	return ctx, nil
}
func (c *client) encrypt(code string) (string, error) {
	encryptedBytes, err := rsa.EncryptPKCS1v15(bytes.NewReader(c.random), c.publicKey, []byte(code))
	if err != nil {
		return "", errors.Wrap(err, "EncryptPKCS1v15 failed")
	}
	newRes := base64.StdEncoding.EncodeToString(encryptedBytes)
	return newRes, nil
}

type targetDept struct {
	FirstDeptCode  string `json:"firstDeptCode"`
	SecondDeptCode string `json:"secondDeptCode"`
	HosCode        string `json:"hosCode"`
}

type targetDeptTime struct {
	targetDept
	Target string `json:"target"`
}

type apiResponse struct {
	ResCode int         `json:"resCode"`
	Msg     string      `json:"msg"`
	Data    interface{} `json:"data"`
}

type detailInfo struct {
	DutyCode string    `json:"dutyCode"`
	Detail   []*detail `json:"detail"`
}

type detail struct {
	UniqProductKey  string    `json:"uniqProductKey"`
	Period          []*period `json:"period"`
	DoctorName      string    `json:"doctorName"`
	DoctorTitleName string    `json:"doctorTitleName"`
	Skill           string    `json:"skill"`
	NCode           string    `json:"ncode"` // &#xf38a stands for 0, use ; to join
	FCode           string    `json:"fcode"`
	haveNumber      bool
}

func (d *detail) isSpecial() bool {
	for _, s := range specialKeys {
		if strings.Contains(d.DoctorTitleName, s) {
			return true
		}
	}
	return false
}

func haveNumber(ncode string, zeroCode ...string) bool {
	zeroCode = append(zeroCode, "&#xf38a")
	fields := utils.SafeSplit(ncode, ";")
	return !(len(fields) == 1 && fields[0] == zeroCode[0])
}

func extraLastCode(s string) string {
	fields := utils.SafeSplit(s, ";")
	return fields[len(fields)-1]
}

type period struct {
	UniqProductKey string `json:"uniqProductKey"`
	DutyTime       string `json:"dutyTime"`
	NCode          string `json:"ncode"` // &#xf38a stands for 0, use ; to join
}

func setEncryptedMobile(ctx context.Context, mobile string) context.Context {
	return context.WithValue(ctx, encryptedMobileKey, mobile)
}

func getEncryptedMobile(ctx context.Context) string {
	return ctx.Value(encryptedMobileKey).(string)
}

func addCookies(ctx context.Context, cookies []*http.Cookie) context.Context {
	org := getCookies(ctx)
	if org == nil {
		org = []*http.Cookie{}
	}

	cs := map[string]*http.Cookie{}
	for _, c := range org {
		cs[c.Name] = c
	}
	for _, c := range cookies {
		cs[c.Name] = c
	}

	res := make([]*http.Cookie, 0, len(cs))
	for _, c := range cs {
		res = append(res, c)
	}

	return context.WithValue(ctx, cookieKey, res)
}

func getCookies(ctx context.Context) []*http.Cookie {
	val := ctx.Value(cookieKey)
	if val == nil {
		return nil
	}
	return val.([]*http.Cookie)
}

func (c *client) getDetail(ctx context.Context, tm time.Time, target *targetDeptTime) ([]*detailInfo, bool, error) {
	resp := []*detailInfo{}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	u := fmt.Sprintf("%v/mobile/product/detail", c.host)
	start := time.Now()
	r, err := c.cli.Post(u,
		httpreq.SetRequestContext(ctx),
		c.addRequestMetaInfo(ctx),
		httpreq.SetRequestQueryValues(map[string][]string{"_time": {strconv.FormatInt(tm.UnixMilli(), 10)}}),
		httpreq.SetRequestBody(target),
		httpreq.AddRequestMiddlewares(addBindResponseMiddleware(&resp), checkResponseErrorMiddleware),
	)
	if err != nil {
		return nil, false, errors.Wrap(err, "get detail failed")
	}
	logrus.Debugf("getDetail cost=%v body=%v", time.Now().Sub(start), r.String())

	residual := false
	filtered := []*detailInfo{}
	for _, d := range resp {
		filteredD := []*detail{}
		for _, dd := range d.Detail {
			zeroCode := extraLastCode(dd.FCode)
			dd.haveNumber = haveNumber(dd.NCode, zeroCode)
			if dd.haveNumber || Conf().ConfigInfo.TryBest {
				residual = true
				filteredD = append(filteredD, dd)
			}
		}
		newD := *d
		newD.Detail = filteredD
		filtered = append(filtered, &newD)
	}

	return filtered, residual, nil
}

type targetDoctor struct {
	targetDeptTime
	UniqProductKey string `json:"uniqProductKey"`
	DutyTime       string `json:"dutyTime,omitempty"` // default 0
}

type confirmData struct {
	NeedRemoteHospitalCard bool            `json:"needRemoteHospitalCard"`
	DataItem               ConfirmDataItem `json:"dataItem"`
	ConfirmToken           string          `json:"confirmToken"`
}

type ConfirmDataItem struct {
	HospitalCardId int `json:"hospitalCardId"`
	SMSCode        int `json:"smsCode"` // smscode <= 2: no need sms code; smscode >=4: must need sms code
}

func (c *client) confirm(ctx context.Context, tm time.Time, targetDoctor *targetDoctor) (*confirmData, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	resp := confirmData{}
	u := fmt.Sprintf("%v/web/product/confirm", c.host)
	start := time.Now()
	r, err := c.cli.Post(u,
		httpreq.SetRequestContext(ctx),
		c.addRequestMetaInfo(ctx),
		httpreq.SetRequestQueryValues(map[string][]string{"_time": {strconv.FormatInt(tm.UnixMilli(), 10)}}),
		httpreq.SetRequestBody(targetDoctor),
		httpreq.AddRequestMiddlewares(addBindResponseMiddleware(&resp), checkResponseErrorMiddleware),
	)
	body := ""
	if r != nil {
		body = r.String()
	}
	logrus.Debugf("confirm cost=%v body=%v", time.Since(start), body)
	if err != nil {
		return nil, errors.Wrap(err, "confirm failed")
	}

	return &resp, nil
}

type getSMSCodeRequest struct {
	Time   int64  `json:"_time" form:"_time"`
	Mobile string `json:"mobile" form:"mobile"`
	SMSKey string `json:"smsKey" form:"smsKey"`
	// for creating order
	UniqProductKey string `json:"uniqProductKey" form:"uniqProductKey"`
	// for login
	Code       string `json:"code" form:"code"`
	DoctorName string `json:"-"`
}

type getSMSCodeForLoginRequest struct {
	Time   int64  `json:"_time" form:"_time"`
	Mobile string `json:"mobile" form:"mobile"`
	SMSKey string `json:"smsKey" form:"smsKey"`
	// for login
	Code string `json:"code" form:"code"`
}

func (c *client) getSMSCodeByCache(ctx context.Context, req *getSMSCodeRequest) string {
	key := fmt.Sprintf("getSMSCode-%v-%v-%v", req.SMSKey, req.UniqProductKey, req.Code)
	if res, ok := c.cache.Load(key); ok {
		return res.(string)
	}
	if _, alreadyExist := c.cacheFlg.LoadOrStore(key, struct{}{}); !alreadyExist {
		go func() {
			defer c.cacheFlg.Delete(key)

			code, err := c.getSMSCode(ctx, req)
			if err != nil {
				logrus.Warningf("getSMSCode failed, err=%v", err)
				return
			}
			c.cache.Store(key, code)
			go time.AfterFunc(time.Minute, func() {
				c.cache.Delete(key)
			})
		}()
	}

	return ""
}

func (c *client) getSMSCode(ctx context.Context, req interface{}) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	logrus.Debugf("startting to get sms code")
	u := fmt.Sprintf("%v/mobile/common/verify-code/send", c.host)
	_, err := c.cli.Get(u,
		httpreq.SetRequestContext(ctx),
		c.addRequestMetaInfo(ctx),
		httpreq.SetRequestQuery(req),
		httpreq.AddRequestMiddlewares(checkResponseErrorMiddleware),
	)
	if err != nil {
		logrus.Debugf("get failed")
		return "", errors.Wrap(err, "get sms code failed")
	}
	fmt.Printf("==> please enter your sms code: ")
	code, err := c.msgGetter.GetCode(ctx, c.msgPattern, time.Now(), Conf().ConfigInfo.ByKB)
	if err != nil {
		return "", err
	}
	return code, nil
}

type PatientID struct {
	IDCard
	Card
}

type IDCard struct {
	IDCardNo   string `json:"idCardNo"`
	IDCardType string `json:"idCardType"`
}

type Card struct {
	CardNo   string `json:"cardNo"`
	CardType string `json:"cardType"`
}

func (c *client) check(ctx context.Context, tm time.Time, id *PatientID) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	u := fmt.Sprintf("%v/web/patient/order/check", c.host)
	_, err := c.cli.Post(u,
		httpreq.SetRequestContext(ctx),
		c.addRequestMetaInfo(ctx),
		httpreq.SetRequestQueryValues(map[string][]string{"_time": {strconv.FormatInt(tm.UnixMilli(), 10)}}),
		httpreq.SetRequestBody(id.IDCard),
		httpreq.AddRequestMiddlewares(checkResponseErrorMiddleware),
	)
	if err != nil {
		return errors.Wrap(err, "check failed")
	}
	return nil
}

type saveRequest struct {
	targetDoctor
	PatientID
	SMSCode        string `json:"smsCode"`
	HospitalCardId string `json:"hospitalCardId"`
	Phone          string `json:"phone"`
	ConfirmToken   string `json:"confirmToken"`
	IsSelf         bool   `json:"isSelf"`
}

type _saveRequest struct {
	targetDept
	DutyTime       string `json:"dutyTime"`
	TreatmentDay   string `json:"treatmentDay"`
	UniqProductKey string `json:"uniqProductKey"`
	CardNo         string `json:"cardNo"`
	CardType       string `json:"cardType"`
	SMSCode        string `json:"smsCode"` // plain text
	HospitalCardId string `json:"hospitalCardId"`
	Phone          string `json:"phone"`
	OrderFrom      string `json:"orderFrom"`
	ContactRelType string `json:"contactRelType,omitempty"`
	ConfirmToken   string `json:"confirmToken"`
	//"contactIdCardNo": "",
	//"contactRelType": "CONTACT_PARENTS",
	//"contactIdCardType": "IDENTITY_CARD",
	//"contactPhone": "",
	//"contactUsername": "",
}

type saveData struct {
	OrderNo string `json:"orderNo"`
	Lineup  bool   `json:"lineup"`
}

func (c *client) save(ctx context.Context, tm time.Time, req *saveRequest) (*saveData, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	resp := saveData{}
	u := fmt.Sprintf("%v/web/order/save", c.host)
	relType := "CONTACT_PARENTS"
	if !req.IsSelf {
		relType = ""
	}
	_, err := c.cli.Post(u,
		httpreq.SetRequestContext(ctx),
		c.addRequestMetaInfo(ctx),
		httpreq.SetRequestQueryValues(map[string][]string{"_time": {strconv.FormatInt(tm.UnixMilli(), 10)}}),
		httpreq.SetRequestBody(&_saveRequest{
			targetDept:     req.targetDept,
			DutyTime:       req.DutyTime,
			TreatmentDay:   req.Target,
			UniqProductKey: req.UniqProductKey,
			CardNo:         "",
			CardType:       req.CardType,
			SMSCode:        req.SMSCode,
			HospitalCardId: req.HospitalCardId,
			Phone:          "",
			OrderFrom:      "OTHER",
			ContactRelType: relType,
			ConfirmToken:   req.ConfirmToken,
		}),
		httpreq.AddRequestMiddlewares(addBindResponseMiddleware(&resp), checkResponseErrorMiddleware),
	)
	if err != nil {
		return nil, errors.Wrap(err, "save failed")
	}
	return &resp, nil
}

func addBindResponseMiddleware(respData interface{}) httpreq.Middleware {
	if reflect.TypeOf(respData).Kind() != reflect.Ptr {
		panic("respData must be ptr")
	}
	return func(next httpreq.EndPoint) httpreq.EndPoint {
		return func(req *httpreq.Request) (*httpreq.Response, error) {
			resp, err := next(req)
			if err != nil {
				return resp, err
			}
			data, _, _, err := jsonparser.Get(resp.Body(), "data")
			if err != nil {
				return resp, fmt.Errorf("invalid resp body, err=%v", err)
			}
			err = binding.Default(resp.Header("Content-Type")).BindBody(data, respData)
			if err != nil {
				return resp, err
			}
			return resp, nil
		}
	}
}

func checkResponseErrorMiddleware(next httpreq.EndPoint) httpreq.EndPoint {
	return func(req *httpreq.Request) (*httpreq.Response, error) {
		resp, err := next(req)
		if err != nil {
			return resp, err
		}
		response := &apiResponse{}
		err = resp.Bind(response)
		if err != nil {
			if resp.StatusCode() != http.StatusOK {
				return resp, errors.New(resp.Status())
			}
			return resp, fmt.Errorf("bind open api resp failed, err=%v", err)
		}
		if response.ResCode != 0 {
			return resp, fmt.Errorf("response res failed, msg=%v", response.Msg)
		}
		return resp, nil
	}
}

func (c *client) addRequestMetaInfo(ctx context.Context) httpreq.RequestOptionFunc {
	return func(req *http.Request) (*http.Request, error) {
		req.Header = http.Header(c.commonHeaders).Clone()
		return req, nil
	}
}
