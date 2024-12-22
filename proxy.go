// main.go
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v2"
)

// Config represents the YAML configuration structure
type Config struct {
	Gateway struct {
		Host             string `yaml:"host"`
		Port             int    `yaml:"port"`
		Username         string `yaml:"username"`
		Password         string `yaml:"password"`
		ZKID            string `yaml:"zkid"`
		DeviceCount     int    `yaml:"device_count"`
		HeartbeatInterval int    `yaml:"heartbeat_interval"`
	} `yaml:"gateway"`
	HTTPServer struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"http_server"`
	HomeAssistant struct {
		Host  string `yaml:"host"`
		Port  int    `yaml:"port"`
		Token string `yaml:"token"`
	} `yaml:"home_assistant"`
	Devices struct {
		Curtains map[string]string `yaml:"curtains"`
		Lights   map[string]string `yaml:"lights"`
	} `yaml:"devices"`
	Logging struct {
		Level string `yaml:"level"`
		File  string `yaml:"file"`
	} `yaml:"logging"`
}

// Message represents a gateway message
type Message struct {
	NodeID    string      `json:"nodeid"`
	Opcode    string      `json:"opcode"`
	Arg       interface{} `json:"arg"`
	Requester string      `json:"requester"`
	ReqID     int64      `json:"reqId,omitempty"`
	Status    string      `json:"status,omitempty"`
}

// Proxy represents the main proxy structure
type Proxy struct {
	config     *Config
	conn       net.Conn
	devices    map[string]string
	entity     map[string]string
	mutex      sync.Mutex
	connected  bool
	handlers   map[string]func(*Message)
}

// NewProxy creates a new proxy instance
func NewProxy(config *Config) *Proxy {
	p := &Proxy{
		config:    config,
		devices:   make(map[string]string),
		entity:    make(map[string]string),
		connected: false,
	}

	p.handlers = map[string]func(*Message){
		"CCU_HB":    p.handleHeartbeat,
		"SYNC_INFO": p.handleSync,
		"SWITCH":    p.handleSwitch,
		"LOGIN":     p.handleLogin,
	}

	return p
}

func (p *Proxy) connect() error {
	addr := fmt.Sprintf("%s:%d", p.config.Gateway.Host, p.config.Gateway.Port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to gateway: %v", err)
	}

	p.conn = conn
	p.connected = true
	fmt.Println("Connected to gateway at %s", addr)
	return p.login()
}

func (p *Proxy) login() error {
	loginMsg := Message{
		NodeID:    "*",
		Opcode:    "LOGIN",
		Requester: "HJ_Server",
		Arg: map[string]string{
			"username": p.config.Gateway.Username,
			"password": p.config.Gateway.Password,
			"zkid":     p.config.Gateway.ZKID,
			"seq":      "",
			"device":   "",
			"version":  "",
		},
	}
	return p.sendMessage(&loginMsg)
}

func (p *Proxy) sendMessage(msg *Message) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	message := fmt.Sprintf("!%s$", string(data))
	_, err = p.conn.Write([]byte(message))
	return err
}

func (p *Proxy) receive() {
	reader := bufio.NewReader(p.conn)
	buffer := ""

	for p.connected {
		data, err := reader.ReadString('$')
		if err != nil {
			fmt.Println("Error reading from connection: %v", err)
			p.handleDisconnect()
			return
		}

		buffer += data
		messages := p.parseMessages(buffer)
		buffer = ""

		for _, msg := range messages {
			p.handleMessage(msg)
		}
	}
}

func (p *Proxy) parseMessages(buffer string) []*Message {
	var messages []*Message
	parts := strings.Split(buffer, "$")

	for _, part := range parts {
		if strings.HasPrefix(part, "!") {
			jsonStr := strings.TrimPrefix(part, "!")
			var msg Message
			if err := json.Unmarshal([]byte(jsonStr), &msg); err == nil {
				messages = append(messages, &msg)
			}
		}
	}

	return messages
}

func (p *Proxy) handleMessage(msg *Message) {
	if handler, ok := p.handlers[msg.Opcode]; ok {
		handler(msg)
	} else {
		fmt.Println("Unhandled message: %v", msg)
	}
}

func (p *Proxy) handleHeartbeat(_ *Message) {
	fmt.Println("收到心跳响应")
}

func (p *Proxy) handleSync(msg *Message) {
	fmt.Println("Received sync response: %v", msg)
}

func (p *Proxy) handleSwitch(msg *Message) {
	nodeID := msg.NodeID
	arg, ok := msg.Arg.(string)
	if !ok {
		return
	}

	p.devices[nodeID] = arg
	var state string

	switch arg {
	case "ON", "OPEN":
		state = "on"
	case "OFF", "CLOSE":
		state = "off"
	default:
		return
	}

	// Get entity ID from config
	var entityID string
	if _, ok := p.config.Devices.Curtains[nodeID]; ok {
		entityID = p.config.Devices.Curtains[nodeID]
	} else if _, ok := p.config.Devices.Lights[nodeID]; ok {
		entityID = p.config.Devices.Lights[nodeID]
	}

	if entityID == "" {
		return
	}

	lastState := p.entity[entityID]
	if lastState == state {
		return
	}

	p.entity[entityID] = state
	p.updateHomeAssistant(fmt.Sprintf("switch.%s", entityID), state)
}

func (p *Proxy) updateHomeAssistant(entityID, state string) {
	url := fmt.Sprintf("http://%s:%d/api/states/%s",
		p.config.HomeAssistant.Host,
		p.config.HomeAssistant.Port,
		entityID)

	data := map[string]string{"state": state}
	jsonData, _ := json.Marshal(data)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+p.config.HomeAssistant.Token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error updating Home Assistant: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		fmt.Println("Successfully updated entity %s to state %s", entityID, state)
	} else {
		fmt.Println("Failed to update Home Assistant: %d", resp.StatusCode)
	}
}

func (p *Proxy) handleLogin(msg *Message) {
	if msg.Status == "success" {
		fmt.Println("Login successful")
	} else {
		fmt.Println("Login failed")
	}
}

func (p *Proxy) sendHeartbeat() {
	heartbeatMsg := &Message{
		NodeID:    "*",
		Opcode:    "CCU_HB",
		Arg:       "*",
		Requester: "HJ_Server",
	}

	for p.connected {
		if err := p.sendMessage(heartbeatMsg); err != nil {
			fmt.Println("Error sending heartbeat: %v", err)
			p.handleDisconnect()
			return
		}
		time.Sleep(time.Duration(p.config.Gateway.HeartbeatInterval) * time.Second)
	}
}

func (p *Proxy) handleDisconnect() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if !p.connected {
		return
	}

	p.connected = false
	if p.conn != nil {
		p.conn.Close()
	}

	fmt.Println("Disconnected from gateway, attempting to reconnect...")
	time.Sleep(10 * time.Second)
	p.reconnect()
}

func (p *Proxy) reconnect() {
	for !p.connected {
		if err := p.connect(); err != nil {
			fmt.Println("Reconnection failed: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}
		go p.receive()
		go p.sendHeartbeat()
		p.initState()
		break
	}
}

func (p *Proxy) initState() {
	for i := 1; i <= p.config.Gateway.DeviceCount; i++ {
		p.queryNodeID(strconv.Itoa(i))
	}
}

func (p *Proxy) queryNodeID(nodeID string) {
	msg := &Message{
		NodeID:    nodeID,
		Opcode:    "QUERY",
		Arg:       "*",
		Requester: "HJ_Server",
		ReqID:     time.Now().Unix(),
	}
	p.sendMessage(msg)
}

func (p *Proxy) Start() error {
	if err := p.connect(); err != nil {
		return err
	}

	go p.receive()
	go p.sendHeartbeat()
	p.initState()

	return nil
}

func main() {
	// Read configuration
	configData, err := ioutil.ReadFile("config.yaml")
	if err != nil {
		fmt.Println("Error reading config file: %v", err)
	}

	var config Config
	if err := yaml.Unmarshal(configData, &config); err != nil {
		fmt.Println("Error parsing config file: %v", err)
	}

	// Initialize proxy
	proxy := NewProxy(&config)
	if err := proxy.Start(); err != nil {
		fmt.Println("Error starting proxy: %v", err)
	}

	// Initialize Gin router
	router := gin.Default()

	// Switch endpoints
	router.POST("/switch/:id", func(c *gin.Context) {
		id := c.Param("id")
		var data struct {
			Arg string `json:"arg"`
		}
		if err := c.BindJSON(&data); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request"})
			return
		}

		msg := &Message{
			NodeID:    id,
			Opcode:    "SWITCH",
			Arg:       data.Arg,
			Requester: "HJ_Server",
			ReqID:     time.Now().Unix(),
		}
		proxy.sendMessage(msg)
		proxy.devices[id] = data.Arg
		c.JSON(200, gin.H{"is_active": data.Arg == "ON"})
	})

	router.GET("/switch/:id", func(c *gin.Context) {
		id := c.Param("id")
		state := proxy.devices[id]
		c.JSON(200, gin.H{"is_active": state == "ON"})
	})

	// Curtain endpoints
	router.POST("/curtain/:id", func(c *gin.Context) {
		id := c.Param("id")
		var data struct {
			Arg string `json:"arg"`
		}
		if err := c.BindJSON(&data); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request"})
			return
		}

		msg := &Message{
			NodeID:    id,
			Opcode:    "SWITCH",
			Arg:       data.Arg,
			Requester: "HJ_Server",
			ReqID:     time.Now().Unix(),
		}
		proxy.sendMessage(msg)
		proxy.devices[id] = data.Arg
		c.JSON(200, gin.H{"is_open": data.Arg == "OPEN"})
	})

	router.GET("/curtain/:id", func(c *gin.Context) {
		id := c.Param("id")
		state := proxy.devices[id]
		c.JSON(200, gin.H{"is_open": state == "OPEN"})
	})

	// Start HTTP server
	addr := fmt.Sprintf("%s:%d", config.HTTPServer.Host, config.HTTPServer.Port)
	if err := router.Run(addr); err != nil {
		fmt.Println("Error starting HTTP server: %v", err)
	}
}