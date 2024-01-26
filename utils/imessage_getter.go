package utils

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	//"gorm.io/driver/sqlite"
	"regexp"

	"github.com/glebarez/sqlite"
	_ "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IMessageGetter struct {
	db *gorm.DB
}

const (
	OSX_EPOCH = 978307200
)

func NewIMessageGetter() (*IMessageGetter, error) {
	user, ok := os.LookupEnv("USER")
	if !ok {
		return nil, fmt.Errorf("not found environment variable USER")
	}
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("/Users/%v/Library/Messages/chat.db", user)), &gorm.Config{})
	if err != nil {
		logrus.Fatalf("open chat.db failed, err=%v", err)
	}
	return &IMessageGetter{db: db}, nil
}

type chat struct {
	Text     string    `gorm:"column:text"`
	Date     int64     `gorm:"column:date"`
	DateTime time.Time `gorm:"-"`
}

func (i *IMessageGetter) GetCode(ctx context.Context, pattern *regexp.Regexp, now time.Time, byKB bool) (string, error) {
	if byKB {
		return i.getCodeByKeyboard()
	}
	return i.getCodeByDB(ctx, pattern, now)
}

func (i *IMessageGetter) getCodeByKeyboard() (string, error) {
	code := ""
	fmt.Printf("<==")
	_, err := fmt.Scanln(&code)
	if err != nil {
		return "", errors.Wrap(err, "scan code failed")
	}
	return code, nil
}

func (i *IMessageGetter) getCodeByDB(ctx context.Context, pattern *regexp.Regexp, now time.Time) (string, error) {
	res := &chat{}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		err := i.db.Table("message").
			Order(clause.OrderByColumn{Column: clause.Column{Name: "date"}, Desc: true}).
			Limit(1).Find(&res).Error
		if err != nil {
			logrus.Warningf("get message from db failed, err=%v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		res.DateTime = dateFromTimestamp(res.Date)

		if res.DateTime.After(now) {
			match := pattern.FindStringSubmatch(res.Text)
			logrus.Debugf("match=%v", match)
			if len(match) == 2 {
				return match[1], nil
			}
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func dateFromTimestamp(t int64) time.Time {
	return time.Unix(t/1e9+OSX_EPOCH, 0)
}
