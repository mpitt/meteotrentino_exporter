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

const urlFmt = "http://dati.meteotrentino.it/service.asmx/ultimiDatiStazione?codice=%s"

type LocalTime struct {
	time.Time
}

func (t *LocalTime) UnmarshalText(data []byte) error {
	d := append(data, []byte("+01:00")...)
	return t.Time.UnmarshalText(d)
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
	RH float64 `xml:"rh"`
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
	interval    = flag.Duration("intervallo", 60*time.Second, "Intervallo di tempo tra le richieste successive. I dati sono aggiornati alla fonte ogni 15 minuti")
	listenAddr  = flag.String("listen-addr", ":8089", "Indirizzo di rete su cui esporre il server HTTP")
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
)

func getRealTimeData() (item *DatiOggi, err error) {
	res, err := http.Get(fmt.Sprintf(urlFmt, *codStazione))
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
		"place":        "Rovereto",
	}
	o, err := getRealTimeData()
	if err != nil {
		log.Println(err)
		tempMetric.DeletePartialMatch(labels)
		rainMetric.DeletePartialMatch(labels)
		humidityMetric.DeletePartialMatch(labels)
		return
	}
	// fmt.Printf("%#v\n", o)

	temps := o.Temperature.TemperaturaAria
	lastTemp := temps[len(temps)-1].Temperatura
	tempMetric.With(labels).Set(lastTemp)

	precs := o.Precipitazioni.Precipitazione
	lastRain := precs[len(precs)-1].Pioggia
	rainMetric.With(labels).Set(lastRain)

	hums := o.Umidita.Umidita
	lastHum := hums[len(hums)-1].RH
	humidityMetric.With(labels).Set(lastHum)
}

func main() {
	flag.Parse()
	log.Println("Getting data from", fmt.Sprintf(urlFmt, *codStazione))
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
