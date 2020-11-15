// Copyright (c) Alex Ellis 2017. All rights reserved.
// Copyright (c) OpenFaaS Project 2018. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/Shopify/sarama"
	cluster "github.com/bsm/sarama-cluster"
	"github.com/openfaas-incubator/connector-sdk/types"
)

var saramaKafkaProtocolVersion = sarama.V0_10_2_0

type connectorConfig struct {
	*types.ControllerConfig
	Topics       []string
	Broker       string
	User         string
	Password     string
	KafkaVersion sarama.KafkaVersion
}

const (
	DEFAULT_KAFKA_PORT = "9092"
)

func main() {

	credentials := types.GetCredentials()
	config := buildConnectorConfig()

	controller := types.NewController(credentials, config.ControllerConfig)

	kafkaConfig := getConfig(config)

	controller.BeginMapBuilder()

	brokers := []string{config.Broker}
	waitForBrokers(brokers, config, kafkaConfig, controller)

	makeConsumer(brokers, config, kafkaConfig, controller)
}

func getConfig(config connectorConfig) *sarama.Config {
	sConfig := sarama.NewConfig()
	sConfig.Net.DialTimeout = 10 * time.Second
	sConfig.Net.SASL.Enable = true
	if len(config.User) > 0 && len(config.Password) > 0 {
		sConfig.Net.SASL.User = config.User
		sConfig.Net.SASL.Password = config.Password
		sConfig.Net.TLS.Enable = true
		sConfig.Net.TLS.Config = &tls.Config{
			InsecureSkipVerify: true,
			ClientAuth:         0,
		}
	}
	sConfig.Version = config.KafkaVersion
	return sConfig
}

func waitForBrokers(brokers []string, config connectorConfig, kafkaConfig *sarama.Config, controller *types.Controller) {

	var client sarama.Client
	var err error

	for {
		if len(controller.Topics()) > 0 {
			client, err = sarama.NewClient(brokers, kafkaConfig)
			if client != nil && err == nil {
				break
			}
			if client != nil {
				client.Close()
			}
			fmt.Println("Wait for brokers ("+config.Broker+") to come up.. ", brokers)
		}

		time.Sleep(1 * time.Second)
	}
}

func makeConsumer(brokers []string, config connectorConfig, kafkaConfig *sarama.Config, controller *types.Controller) {
	//setup consumer
	cConfig := cluster.NewConfig()
	cConfig.Config = *kafkaConfig
	cConfig.Consumer.Return.Errors = true
	cConfig.Consumer.Offsets.Initial = sarama.OffsetNewest //OffsetOldest
	cConfig.Group.Return.Notifications = true
	cConfig.Group.Session.Timeout = 6 * time.Second
	cConfig.Group.Heartbeat.Interval = 2 * time.Second

	group := "faas-kafka-queue-workers"

	topics := config.Topics
	log.Printf("Binding to topics: %v", config.Topics)

	consumer, err := cluster.NewConsumer(brokers, group, topics, cConfig)
	if err != nil {
		log.Fatalln("Fail to create Kafka consumer: ", err)
	}

	defer consumer.Close()

	num := 0

	for {
		select {
		case msg, ok := <-consumer.Messages():
			if ok {
				num = (num + 1) % math.MaxInt32
				fmt.Printf("[#%d] Received on [%v,%v]: '%s'\n",
					num,
					msg.Topic,
					msg.Partition,
					string(msg.Value))

				controller.Invoke(msg.Topic, &msg.Value)

				consumer.MarkOffset(msg, "") // mark message as processed
			}
		case err = <-consumer.Errors():

			fmt.Println("consumer error: ", err)

		case ntf := <-consumer.Notifications():

			fmt.Printf("Rebalanced: %+v\n", ntf)

		}
	}
}

func buildConnectorConfig() connectorConfig {

	broker := "kafka:9092"
	if val, exists := os.LookupEnv("broker_host"); exists {
		broker = val
	}

	topics := []string{}
	if val, exists := os.LookupEnv("topics"); exists {
		for _, topic := range strings.Split(val, ",") {
			if len(topic) > 0 {
				topics = append(topics, topic)
			}
		}
	}
	if len(topics) == 0 {
		log.Fatal(`Provide a list of topics i.e. topics="payment_published,slack_joined"`)
	}

	gatewayURL := "http://gateway:8080"
	if val, exists := os.LookupEnv("gateway_url"); exists {
		gatewayURL = val
	}

	upstreamTimeout := time.Second * 30
	rebuildInterval := time.Second * 3

	if val, exists := os.LookupEnv("upstream_timeout"); exists {
		parsedVal, err := time.ParseDuration(val)
		if err == nil {
			upstreamTimeout = parsedVal
		}
	}

	if val, exists := os.LookupEnv("rebuild_interval"); exists {
		parsedVal, err := time.ParseDuration(val)
		if err == nil {
			rebuildInterval = parsedVal
		}
	}

	printResponse := false
	if val, exists := os.LookupEnv("print_response"); exists {
		printResponse = (val == "1" || val == "true")
	}

	printResponseBody := false
	if val, exists := os.LookupEnv("print_response_body"); exists {
		printResponseBody = (val == "1" || val == "true")
	}

	delimiter := ","
	if val, exists := os.LookupEnv("topic_delimiter"); exists {
		if len(val) > 0 {
			delimiter = val
		}
	}

	asynchronousInvocation := false
	if val, exists := os.LookupEnv("asynchronous_invocation"); exists {
		asynchronousInvocation = (val == "1" || val == "true")
	}

	user := ""
	if val, exists := os.LookupEnv("user"); exists {
		user = val
	}

	password := ""
	if val, exists := os.LookupEnv("password"); exists {
		password = val
	}

	kafkaVersion := sarama.V0_10_2_0
	if val, exists := os.LookupEnv("kafka_version"); exists {
		parsedKafkaVersion, errorVersion := sarama.ParseKafkaVersion(val)
		if errorVersion != nil {
			log.Fatalln("Invalid Kafka version: ", errorVersion)
		}

		kafkaVersion = parsedKafkaVersion
	}

	return connectorConfig{
		ControllerConfig: &types.ControllerConfig{
			UpstreamTimeout:          upstreamTimeout,
			GatewayURL:               gatewayURL,
			PrintResponse:            printResponse,
			PrintResponseBody:        printResponseBody,
			RebuildInterval:          rebuildInterval,
			TopicAnnotationDelimiter: delimiter,
			AsyncFunctionInvocation:  asynchronousInvocation,
		},
		Topics:       topics,
		Broker:       broker,
		User:         user,
		Password:     password,
		KafkaVersion: kafkaVersion,
	}
}
