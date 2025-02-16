# meteotrentino_exporter

Exporter Prometheus per i dati meteo della Provincia Autonoma di Trento, disponibili sul portale
https://dati.trentino.it.

## Usage

```
  -intervallo duration
    	Intervallo di tempo tra le richieste successive. I dati sono aggiornati alla fonte ogni 15 minuti (default 1m0s)
  -listen-addr string
    	Indirizzo di rete su cui esporre il server HTTP (default ":8089")
  -localita string
    	Località della stazione meteo (default "Rovereto")
  -precipitazione
    	Abilita o disabilita le precipitazioni (default true)
  -stazione string
    	Codice della stazione meteo, si veda anagrafica http://dati.meteotrentino.it/service.asmx/listaStazioni (default "T0147")
  -temperatura
    	Abilita o disabilita le temperature (default true)
  -umidita
    	Abilita o disabilita l'umidità (default true)
  -url-schema string
    	Schema dell'URL da cui ottenere i dati (http o https) (default "https")
```
