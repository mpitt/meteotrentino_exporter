package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const urlFmt = "%s://dati.meteotrentino.it/service.asmx/ultimiDatiStazione?codice=%s"

type LocalTime struct {
	time.Time
}

func (t *LocalTime) UnmarshalText(data []byte) (err error) {
	dataStr := string(data)
	location := time.FixedZone("UTC+1", 1*60*60) // MeteoTrentino always uses CET, even during DST
	t.Time, err = time.ParseInLocation("2006-01-02T15:04:05", dataStr, location)
	return err
}

type TemperaturaAria struct {
	Data        LocalTime `xml:"data"`
	Temperatura float64   `xml:"temperatura"`
	UM          string    `xml:"UM,attr"`
}
type Temperature struct {
	TemperaturaAria []TemperaturaAria `xml:"temperatura_aria"`
}

type Precipitazione struct {
	Data    LocalTime `xml:"data"`
	Pioggia float64   `xml:"pioggia"`
	UM      string    `xml:"UM,attr"`
}
type Precipitazioni struct {
	Precipitazione []Precipitazione `xml:"precipitazione"`
}

type UmiditaRelativa struct {
	Data LocalTime `xml:"data"`
	RH   float64   `xml:"rh"`
}
type UmiditaList struct {
	Umidita []UmiditaRelativa `xml:"umidita_relativa"`
}

type DatiOggi struct {
	Temperature    Temperature    `xml:"temperature"`
	Precipitazioni Precipitazioni `xml:"precipitazioni"`
	Umidita        UmiditaList    `xml:"umidita_relativa"`
}

var (
	codStazione = flag.String("stazione", "T0147", "Codice della stazione meteo, si veda anagrafica http://dati.meteotrentino.it/service.asmx/listaStazioni")
	locStazione = flag.String("localita", "Rovereto", "Località della stazione meteo")
	interval    = flag.Duration("intervallo", 60*time.Second, "Intervallo di tempo tra le richieste successive. I dati sono aggiornati alla fonte ogni 15 minuti")
	listenAddr  = flag.String("listen-addr", ":8089", "Indirizzo di rete su cui esporre il server HTTP")
	urlSchema   = flag.String("url-schema", "https", "Schema dell'URL da cui ottenere i dati (http o https)")
	toggleTemp = flag.Bool("temperatura", true, "Abilita o disabilita le temperature")
	toggleRain = flag.Bool("precipitazione", true, "Abilita o disabilita le precipitazioni")
	toggleHum = flag.Bool("umidita", true, "Abilita o disabilita l'umidità")
	url         string
	errMetricDisabled = errors.New("Metric is disabled")
	tempMetric  = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "temperature_celsius",
			Help: "Current outside temperature in degrees Celsius",
		},
		[]string{"station_code", "place"},
	)
	rainMetric = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rain_mm",
			Help: "Amount of rain in the last period in mm",
		},
		[]string{"station_code", "place"},
	)
	humidityMetric = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "humidity_percent",
			Help: "Relative himidity in percentage",
		},
		[]string{"station_code", "place"},
	)
	stationsUpMetric = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "stations_up",
			Help: "Number of stations successfully queried",
		},
	)
)

func getRealTimeData() (item *DatiOggi, err error) {
	res, err := http.Get(url)
	if err != nil {
		return
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode > 299 {
		err = fmt.Errorf(
			"Response failed with status code: %d and\nbody: %s\n", res.StatusCode, body)
	}
	if err != nil {
		return
	}

	item = &DatiOggi{}
	err = xml.Unmarshal(body, item)
	if err != nil {
		return
	}
	log.Println("Received and parsed data")
	return
}

func refreshTemp(s []TemperaturaAria, lastAcceptableTimestamp time.Time) (value float64, err error) {
	if !*toggleTemp {
		err = fmt.Errorf("%w: temperature", errMetricDisabled)
		return
	}
	n := len(s)
	if n < 1 {
		err = fmt.Errorf("No samples in temperature series")
		return
	}
	last := s[n-1]
	if !last.Data.Time.After(lastAcceptableTimestamp) {
		err = fmt.Errorf(
			"Rejected stale temperature sample with timestamp %v (current time %v)", last.Data, time.Now().Format(time.RFC3339))
		return
	}
	value = last.Temperatura
	return
}

func refreshRain(s []Precipitazione, lastAcceptableTimestamp time.Time) (value float64, err error) {
	if !*toggleRain {
		err = fmt.Errorf("%w: rain", errMetricDisabled)
		return
	}
	n := len(s)
	if n < 1 {
		err = fmt.Errorf("No samples in rain series")
		return
	}
	last := s[n-1]
	if !last.Data.Time.After(lastAcceptableTimestamp) {
		err = fmt.Errorf(
			"Rejected stale rain sample with timestamp %v (current time %v)", last.Data, time.Now().Format(time.RFC3339))
		return
	}
	value = last.Pioggia
	return
}

func refreshHum(s []UmiditaRelativa, lastAcceptableTimestamp time.Time) (value float64, err error) {
	if !*toggleHum {
		err = fmt.Errorf("%w: humidity", errMetricDisabled)
		return
	}
	n := len(s)
	if n < 1 {
		err = fmt.Errorf("No samples in humidity series")
		return
	}
	last := s[n-1]
	if !last.Data.Time.After(lastAcceptableTimestamp) {
		err = fmt.Errorf(
			"Rejected stale humidity sample with timestamp %v (current time %v)", last.Data, time.Now().Format(time.RFC3339))
		return
	}
	value = last.RH
	return
}

func logMetricError(err error) {
	if !errors.Is(err, errMetricDisabled) { log.Println(err) }
}

func refresh() {
	labels := prometheus.Labels{
		"station_code": *codStazione,
		"place":        *locStazione,
	}
	var updated float64 = 0
	var value float64
	now := time.Now()
	lastAcceptableTimestamp := now.Add(-30 * time.Minute)

	o, err := getRealTimeData()
	if err != nil {
		log.Println(err)
		tempMetric.DeletePartialMatch(labels)
		rainMetric.DeletePartialMatch(labels)
		humidityMetric.DeletePartialMatch(labels)
		stationsUpMetric.Set(0)
		return
	}
	// fmt.Printf("%#v\n", o)

	temps := o.Temperature.TemperaturaAria
	value, err = refreshTemp(temps, lastAcceptableTimestamp)
	if err != nil {
		logMetricError(err)
		tempMetric.DeletePartialMatch(labels)
	} else {
		tempMetric.With(labels).Set(value)
		updated = 1
	}

	precs := o.Precipitazioni.Precipitazione
	value, err = refreshRain(precs, lastAcceptableTimestamp)
	if err != nil {
		logMetricError(err)
		rainMetric.DeletePartialMatch(labels)
	} else {
		rainMetric.With(labels).Set(value)
		updated = 1
	}

	hums := o.Umidita.Umidita
	value, err = refreshHum(hums, lastAcceptableTimestamp)
	if err != nil {
		logMetricError(err)
		humidityMetric.DeletePartialMatch(labels)
	} else {
		humidityMetric.With(labels).Set(value)
		updated = 1
	}

	stationsUpMetric.Set(updated)
}

func main() {
	flag.Parse()
	if !(*toggleTemp || *toggleRain || *toggleHum) {
		log.Println("No metric enabled, closing")
		return
	}
	url = fmt.Sprintf(urlFmt, *urlSchema, *codStazione)
	log.Println("Getting data from", url)
	go refresh()
	tick := time.NewTicker(*interval)
	go func() {
		for {
			select {
			case <-tick.C:
				refresh()
			}
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}
