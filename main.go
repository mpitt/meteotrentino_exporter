package main

import (
	"encoding/xml"
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

func (t *LocalTime) UnmarshalText(data []byte) error {
	dataStr := string(data)
	location, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		return err
	}
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
	url         string
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

func refresh() {
	labels := prometheus.Labels{
		"station_code": *codStazione,
		"place":        *locStazione,
	}
	var updated float64 = 0
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
	lastTemp := temps[len(temps)-1]
	if lastTemp.Data.Time.After(lastAcceptableTimestamp) {
		tempMetric.With(labels).Set(lastTemp.Temperatura)
		updated = 1
	} else {
		log.Println("Rejected stale temperature sample with timestamp", lastTemp.Data)
		log.Println("Current time", now.Format(time.RFC3339))
		tempMetric.DeletePartialMatch(labels)
	}

	precs := o.Precipitazioni.Precipitazione
	lastRain := precs[len(precs)-1]
	if lastRain.Data.Time.After(lastAcceptableTimestamp) {
		rainMetric.With(labels).Set(lastRain.Pioggia)
		updated = 1
	} else {
		log.Println("Rejected stale rain sample with timestamp", lastRain.Data)
		log.Println("Current time", now.Format(time.RFC3339))
		rainMetric.DeletePartialMatch(labels)
	}

	hums := o.Umidita.Umidita
	lastHum := hums[len(hums)-1]
	if lastHum.Data.Time.After(lastAcceptableTimestamp) {
		humidityMetric.With(labels).Set(lastHum.RH)
		updated = 1
	} else {
		log.Println("Rejected stale humidity sample with timestamp", lastHum.Data)
		log.Println("Current time", now.Format(time.RFC3339))
		humidityMetric.DeletePartialMatch(labels)
	}

	stationsUpMetric.Set(updated)
}

func main() {
	flag.Parse()
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
