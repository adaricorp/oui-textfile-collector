# oui-textfile-collector

This tool periodically downloads the IEEE OUI database and converts it to a file that can be used by
Node Exporter's [textfile collector](https://github.com/prometheus/node_exporter#textfile-collector).

## Downloading

Download prebuilt binaries from [GitHub](https://github.com/adaricorp/oui-textfile-collector/releases/latest).

## Running

To run oui-textfile-collector and have it output to a custom path:

```
oui_textfile_collector \
    --output-file /var/lib/node_exporter/textfile/oui.prom
```

It is also possible to configure oui-textfile-collector by using environment variables:

```
OUI_TEXTFILE_COLLECTOR_REFRESH_INTERVAL="24h" oui_textfile_collector
```

## Metrics

### Prometheus

With the default configuration, this textfile collector creates one prometheus metric: `mac_oui_info`.
