package main

import "C"
import (
	"encoding/json"
	"fmt"
	"github.com/cloudflare/cloudflare-go"
	"github.com/marpaia/graphite-golang"
	"golang.org/x/net/context"
	"golang.org/x/time/rate"
	"gopkg.in/robfig/cron.v2"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

const (
	CloudFlareAPIRoot = "https://api.cloudflare.com/client/v4/"
	key               = "f73d2fd09a50dd1234a26d37e794de982fc0c"
	email             = "api.sre@lastminute.com"
	orgId             = "f5fd3b3741817e2080883b52b5995643"
)

var logger = log.New(os.Stdout, "[HEIMDALL] ", log.LstdFlags)
var rateLimiter = rate.NewLimiter(rate.Limit(4), 1)

type DataAggregation struct {
	ZoneName string
	ZoneID   string
	Date     time.Time

	TotalRequestAll        int
	TotalRequestCached     int
	TotalRequestUncached   int
	HTTPStatus             map[string]int
	TotalBandwidthAll      int
	TotalBandwidthCached   int
	TotalBandwidthUncached int
	TotalUniquesAll        int
	//WafTrigger       map[string]int
	//RateLimitTrigger map[string]int
}

type zoneAnalyticsColocationResponse struct {
	cloudflare.Response
	Result []cloudflare.ZoneAnalyticsColocation
}

var client = &http.Client{
	Timeout: time.Duration(5 * time.Second),
}

func main() {
	cronExp := "0 * * * * *"
	//cronExp := "* * * * * *"
	logger.Printf("start collecting data %s", cronExp)

	c := cron.New()
	c.AddFunc(cronExp, collectingData)

	go c.Start()
	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt, os.Kill)
	s := <-sig

	c.Stop()
	fmt.Println("Got signal:", s)

	//collectingData()

}

func collectingData() {
	logger.Printf("get zones")
	aggregations, _ := getZonesId(cloudflareClient())
	aggregations, _ = getColocationTotals(aggregations)

	pushMetrics(aggregations)
}

func getZonesId(client *cloudflare.API) ([]*DataAggregation, error) {
	zones, err := client.ListZones()
	if err != nil {
		logger.Printf("ERROR ZoneName from CF Client %v", zones)
		return nil, err
	}

	result := make([]*DataAggregation, 0)
	for _, zone := range zones {
		result = append(result, &DataAggregation{
			ZoneName:   zone.Name,
			ZoneID:     zone.ID,
			HTTPStatus: map[string]int{"2xx": 0, "3xx": 0, "4xx": 0, "5xx": 0},
		})
	}

	return result, nil
}

func getColocationTotals(dataAggregations []*DataAggregation) ([]*DataAggregation, error) {
	for _, data := range dataAggregations {
		logger.Printf("collecting metrics for %s", data.ZoneName)

		zoneAnalyticsDataArray, err := callColocationAnalyticsAPI(data.ZoneID)

		if err != nil {
			logger.Printf("ERROR Getting ZoneName Analytics for zone %v, %v", data.ZoneName, err)
			return nil, err
		}
		for _, zoneAnalyticsData := range zoneAnalyticsDataArray {
			for _, timeSeries := range zoneAnalyticsData.Timeseries {
				data.Date = timeSeries.Until
				data.TotalRequestAll += timeSeries.Requests.All
				data.TotalRequestCached += timeSeries.Requests.Cached
				data.TotalRequestUncached += timeSeries.Requests.Uncached
				data.TotalBandwidthAll += timeSeries.Bandwidth.All
				data.TotalBandwidthCached += timeSeries.Bandwidth.Cached
				data.TotalBandwidthUncached += timeSeries.Bandwidth.Uncached

				data.HTTPStatus = totals(timeSeries.Requests.HTTPStatus, data.HTTPStatus)
			}
		}
	}
	return dataAggregations, nil
}

func totals(source, target map[string]int) map[string]int {

	for k, v := range source {
		key := getKey(k)
		if value, present := target[key]; present {
			value += v
			target[key] = value
		} else {
			target[key] = v
		}
	}
	return target
}
func getKey(httpCode string) string {
	if strings.HasPrefix(httpCode, "2") {
		return "2xx"
	}
	if strings.HasPrefix(httpCode, "3") {
		return "3xx"
	}
	if strings.HasPrefix(httpCode, "4") {
		return "4xx"
	}
	if strings.HasPrefix(httpCode, "5") {
		return "5xx"
	}

	return "1xx"
}

func callColocationAnalyticsAPI(zoneID string) ([]cloudflare.ZoneAnalyticsColocation, error) {
	url := fmt.Sprintf(CloudFlareAPIRoot+"zones/%s/analytics/colos?since=%s&until=%s&continuous=%s", zoneID, "-1", "-1", "false")
	request, _ := http.NewRequest(http.MethodGet, url, nil)

	resp, err := doHttpCall(request)
	if err != nil {
		return nil, fmt.Errorf("get zones HTTP call error: %v", err)
	}
	response := zoneAnalyticsColocationResponse{}
	b, _ := ioutil.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &response); err != nil {
		return nil, fmt.Errorf("HTTP body marshal to JSON error: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		return response.Result, nil
	}
	return nil, fmt.Errorf("getZoneID HTTP error %d", resp.StatusCode)
}

func cloudflareClient() *cloudflare.API {
	c, err := cloudflare.New(key, email, cloudflare.UsingOrganization(orgId), cloudflare.UsingRateLimit(2), cloudflare.HTTPClient(client))
	if err != nil {
		logger.Fatalf("could not create client for Cloudflare: %v", err)
	}
	return c
}

func doHttpCall(request *http.Request) (*http.Response, error) {
	rateLimiter.Wait(context.TODO())
	request = setHeaders(request)
	return client.Do(request)
}

func setHeaders(request *http.Request) *http.Request {
	for key, value := range createHeaders() {
		request.Header.Set(key, value)
	}
	return request
}

func createHeaders() map[string]string {
	return map[string]string{
		"X-Auth-Email": email,
		"X-Auth-Key":   key,
		"Content-Type": "application/json",
	}
}

func pushMetrics(datas []*DataAggregation) {

	newGraphite, err := graphite.NewGraphite("10.120.172.134", 2113)

	if err != nil {
		newGraphite = graphite.NewGraphiteNop("10.120.172.134", 2113)
	}

	metrics := make([]graphite.Metric, 0)
	for _, data := range datas {
		metrics = append(metrics, metric(data.ZoneName, "total.requests.all", strconv.Itoa(data.TotalRequestAll), data.Date))
		metrics = append(metrics, metric(data.ZoneName, "total.requests.cached", strconv.Itoa(data.TotalRequestCached), data.Date))
		metrics = append(metrics, metric(data.ZoneName, "total.requests.uncached", strconv.Itoa(data.TotalRequestUncached), data.Date))
		metrics = append(metrics, metric(data.ZoneName, "total.bandwidth.all", strconv.Itoa(data.TotalBandwidthAll), data.Date))
		metrics = append(metrics, metric(data.ZoneName, "total.bandwidth.cached", strconv.Itoa(data.TotalBandwidthCached), data.Date))
		metrics = append(metrics, metric(data.ZoneName, "total.bandwidth.uncached", strconv.Itoa(data.TotalBandwidthUncached), data.Date))

		for httpFamily, counter := range data.HTTPStatus {
			metrics = append(metrics, metric(data.ZoneName, "total.requests.http_status."+httpFamily, strconv.Itoa(counter), data.Date))
		}
	}
	newGraphite.SendMetrics(metrics)
}

func metric(zone, key, value string, date time.Time) graphite.Metric {
	metricKey := strings.ToLower("cloudflare.new." + strings.Replace(zone, ".", "_", -1) + "." + key)

	logger.Printf("added metric %s, value %s, %v", metricKey, value, date.Unix())

	return graphite.NewMetric(metricKey, value, date.Unix())
}