package waf

import (
	"github.com/lastminutedotcom/heimdall/pkg/model"
	"net/http"
	"time"
)

type WafsClient interface {
	GetWafTriggersBy(zoneID string, since, until time.Time, callCount int) ([]model.WafTrigger, error)

	getWafTrigger(zoneID, nextPageId string) ([]model.WafTrigger, string, error)

	nextWafTriggersBy(triggers []model.WafTrigger, result []model.WafTrigger, zoneID, nextPageId string, since, until time.Time, callCount int) []model.WafTrigger

	callWafTrigger(url string) (*http.Response, model.WAFResponse, error)
}
