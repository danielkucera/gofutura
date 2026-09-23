package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
)

type MQTTPublisher struct {
	client      mqtt.Client
	topicPrefix string
}

func NewMQTTPublisher(brokerURL, user, pass, topicPrefix string) (*MQTTPublisher, error) {
	trimmedBroker := strings.TrimSpace(brokerURL)
	if trimmedBroker == "" {
		return nil, fmt.Errorf("mqtt broker url is empty")
	}
	trimmedPrefix := strings.Trim(strings.TrimSpace(topicPrefix), "/")
	if trimmedPrefix == "" {
		trimmedPrefix = "gofutura/metrics"
	}

	options := mqtt.NewClientOptions()
	options.AddBroker(trimmedBroker)
	options.SetUsername(user)
	options.SetPassword(pass)
	options.SetAutoReconnect(true)
	options.SetConnectRetry(true)
	options.SetConnectRetryInterval(5 * time.Second)
	options.SetOrderMatters(false)
	options.SetClientID(fmt.Sprintf("gofutura-%d", time.Now().UnixNano()))
	options.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("MQTT connection lost: %v", err)
	})

	client := mqtt.NewClient(options)
	token := client.Connect()
	if ok := token.WaitTimeout(10 * time.Second); !ok {
		return nil, fmt.Errorf("timed out connecting to MQTT broker %s", trimmedBroker)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("connect to MQTT broker %s: %w", trimmedBroker, err)
	}

	log.Printf("Connected to MQTT broker %s with topic prefix %s", trimmedBroker, trimmedPrefix)
	return &MQTTPublisher{client: client, topicPrefix: trimmedPrefix}, nil
}

func (p *MQTTPublisher) Close() {
	if p == nil || p.client == nil {
		return
	}
	p.client.Disconnect(250)
}

func (p *MQTTPublisher) PublishGathered() error {
	if p == nil || p.client == nil {
		return nil
	}
	if !p.client.IsConnected() {
		return fmt.Errorf("mqtt client is not connected")
	}

	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return fmt.Errorf("gather prometheus metrics: %w", err)
	}

	for _, family := range metricFamilies {
		metricName := family.GetName()
		for _, metric := range family.Metric {
			topic := p.metricTopic(metricName, metric.GetLabel())
			payload, err := metricPayload(family.GetType(), metric)
			if err != nil {
				return fmt.Errorf("encode metric %s: %w", metricName, err)
			}
			token := p.client.Publish(topic, 0, true, payload)
			if ok := token.WaitTimeout(5 * time.Second); !ok {
				return fmt.Errorf("timed out publishing metric %s to topic %s", metricName, topic)
			}
			if err := token.Error(); err != nil {
				return fmt.Errorf("publish metric %s to topic %s: %w", metricName, topic, err)
			}
		}
	}

	return nil
}

func (p *MQTTPublisher) metricTopic(metricName string, labels []*io_prometheus_client.LabelPair) string {
	parts := []string{p.topicPrefix, metricName}
	if len(labels) == 0 {
		return strings.Join(parts, "/")
	}

	sorted := append([]*io_prometheus_client.LabelPair(nil), labels...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].GetName() < sorted[j].GetName()
	})
	for _, label := range sorted {
		parts = append(parts, sanitizeTopicPart(label.GetName()), sanitizeTopicPart(label.GetValue()))
	}
	return strings.Join(parts, "/")
}

func metricPayload(metricType io_prometheus_client.MetricType, metric *io_prometheus_client.Metric) (string, error) {
	switch metricType {
	case io_prometheus_client.MetricType_GAUGE:
		return formatFloat(metric.GetGauge().GetValue()), nil
	case io_prometheus_client.MetricType_COUNTER:
		return formatFloat(metric.GetCounter().GetValue()), nil
	case io_prometheus_client.MetricType_UNTYPED:
		return formatFloat(metric.GetUntyped().GetValue()), nil
	case io_prometheus_client.MetricType_SUMMARY:
		payload := map[string]any{
			"sample_count": metric.GetSummary().GetSampleCount(),
			"sample_sum":   metric.GetSummary().GetSampleSum(),
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(body), nil
	case io_prometheus_client.MetricType_HISTOGRAM:
		payload := map[string]any{
			"sample_count": metric.GetHistogram().GetSampleCount(),
			"sample_sum":   metric.GetHistogram().GetSampleSum(),
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(body), nil
	default:
		return "", fmt.Errorf("unsupported metric type %s", metricType.String())
	}
}

func formatFloat(v float64) string {
	return fmt.Sprintf("%g", v)
}

func sanitizeTopicPart(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "+", "_", "#", "_", " ", "_")
	return replacer.Replace(trimmed)
}