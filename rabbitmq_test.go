package GoEventBus

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestRabbitEventStore_Commit(t *testing.T) {
	ctx := context.Background()
	// Define a container request for RabbitMQ
	req := testcontainers.ContainerRequest{
		Image:        "rabbitmq:3", // or "rabbitmq:3-management" if you need the management UI
		ExposedPorts: []string{"5672/tcp"},
		WaitingFor:   wait.ForListeningPort("5672/tcp"),
	}
	// Start the RabbitMQ container
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start RabbitMQ container: %v", err)
	}
	defer container.Terminate(ctx)

	// Retrieve host and mapped port for connecting to RabbitMQ
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5672")
	if err != nil {
		t.Fatalf("failed to get mapped port: %v", err)
	}
	rabbitURL := fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port())

	// Set up a dummy dispatcher with a handler that increments callCount
	var callCount int
	dummyHandler := func(args map[string]interface{}) (Result, error) {
		callCount++
		return Result{Message: "handled"}, nil
	}
	dispatcher := RabbitDispatcher{
		"testProjection": dummyHandler,
	}

	// Create a RabbitEventStore using the container's connection details and a queue named "testQueue"
	store, err := NewRabbitEventStore(&dispatcher, rabbitURL, "testQueue")
	if err != nil {
		t.Fatalf("failed to create RabbitEventStore: %v", err)
	}
	defer store.RabbitChannel.Close()
	defer store.RabbitConn.Close()

	// Create and publish an event
	event := NewEvent("testProjection", map[string]interface{}{"key": "value"})
	store.Publish(event)

	// Allow some time for the message to be enqueued
	time.Sleep(1 * time.Second)

	// Retrieve the message from the queue
	msg, ok, err := store.RabbitChannel.Get("testQueue", true)
	if err != nil {
		t.Fatalf("failed to get message from queue: %v", err)
	}
	if !ok {
		t.Fatal("expected a message in the queue but found none")
	}

	var receivedEvent Event
	err = json.Unmarshal(msg.Body, &receivedEvent)
	if err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}

	// Process the retrieved event
	err = store.Commit(&receivedEvent)
	if err != nil {
		t.Errorf("Commit failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected handler to be called once, got %d", callCount)
	}
}

func TestRabbitEventStore_Broadcast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "rabbitmq:3",
		ExposedPorts: []string{"5672/tcp"},
		WaitingFor:   wait.ForListeningPort("5672/tcp"),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start RabbitMQ container: %v", err)
	}
	defer container.Terminate(ctx)

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5672")
	if err != nil {
		t.Fatalf("failed to get mapped port: %v", err)
	}
	rabbitURL := fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port())

	// Set up a dummy handler that signals when the event is processed.
	callCount := 0
	done := make(chan struct{})
	dummyHandler := func(args map[string]interface{}) (Result, error) {
		callCount++
		close(done) // signal that the event has been handled.
		return Result{Message: "handled"}, nil
	}
	dispatcher := RabbitDispatcher{
		"testProjection": dummyHandler,
	}

	store, err := NewRabbitEventStore(&dispatcher, rabbitURL, "testQueue")
	if err != nil {
		t.Fatalf("failed to create RabbitEventStore: %v", err)
	}
	defer store.RabbitChannel.Close()
	defer store.RabbitConn.Close()

	// Start Broadcast in a separate goroutine with cancellation support.
	go store.Broadcast(ctx)

	// Publish an event.
	event := NewEvent("testProjection", map[string]interface{}{"key": "value"})
	store.Publish(event)

	// Wait for the dummy handler to process the event or timeout.
	select {
	case <-done:
		// Event was processed.
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for event to be processed")
	}

	// Cancel the Broadcast goroutine to ensure a clean shutdown.
	cancel()

	if callCount != 1 {
		t.Errorf("expected handler to be called once, got %d", callCount)
	}
}
