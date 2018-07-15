package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	yaml "gopkg.in/yaml.v2"
)

var vizceral *Vizceral
var mutex = &sync.Mutex{}

func main() {
	vizceral = new(Vizceral)
	vizceral.NewVizceral()

	fs := http.FileServer(http.Dir("dist"))
	http.Handle("/", fs)
	http.HandleFunc("/log/complete/", logCompletedConnection)
	http.HandleFunc("/log/failed/", logFailedConnection)
	http.HandleFunc("/get", get)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// VizceralNode holds the metadata for a given app tier
type VizceralNode struct {
	Name      string `json:"name"`
	Renderer  string `json:"renderer"`
	MaxVolume int    `json:"maxVolume"`
	Updated   int32  `json:"updated"`
}

// Metrics holds the count of traffic split into buckets
type Metrics struct {
	Normal  int `json:"normal"`
	Danger  int `json:"danger"`
	Warning int `json:"warning"`
}

// Sum returns the total count of observations for a set of Metrics
func (m Metrics) Sum() int {
	return m.Normal + m.Warning + m.Danger
}

// VizceralConnection holds the stats for a given src:dst pair
// shadowMetrics holds the current minutes accumulating stats
// Metrics holds the previous minutes complete stats
type VizceralConnection struct {
	Source        string  `json:"source"`
	Target        string  `json:"target"`
	Metrics       Metrics `json:"metrics"`
	shadowMetrics Metrics
}

// VizceralNodes holds a map of VizceralNode
// Each node key should be an app tier
// VizceralNodes implements the MashalJSON interface to
// convert the connections into a flat list
type VizceralNodes struct {
	nodes map[string]*VizceralNode
}

// VizceralConnections holds a map of VizceralMetrics
// Each connection key should be a src:dst pair
// VizceralConnections implements the MashalJSON interface to
// convert the connections into a flat list
type VizceralConnections struct {
	connections map[string]*VizceralConnection
}

// Vizceral is a data structure that holds the traffic graph
type Vizceral struct {
	config        Config
	Name          string               `json:"name"`
	Renderer      string               `json:"renderer"`
	Layout        string               `json:"layout"`
	MaxVolume     int                  `json:"maxVolume"`
	Updated       int32                `json:"updated"`
	NodeMap       *VizceralNodes       `json:"nodes"`
	ConnectionMap *VizceralConnections `json:"connections"`
}

// NewVizceral returns a new Vizceral object
func (v *Vizceral) NewVizceral() *Vizceral {
	v.Name = "Bottle application map"
	v.Renderer = "region"
	v.Layout = "ringCenter"
	v.MaxVolume = 0
	v.NodeMap = new(VizceralNodes)
	v.NodeMap.nodes = make(map[string]*VizceralNode)
	v.ConnectionMap = new(VizceralConnections)
	v.ConnectionMap.connections = make(map[string]*VizceralConnection)

	v.config.getConfig()
	v.createScenario()
	go v.snapshotLoop()
	return v
}

func (v *Vizceral) createScenario() {
	for tierName, tier := range v.config.Ships {
		node := &VizceralNode{}
		node.Name = tierName
		node.Renderer = "region"
		vizceral.NodeMap.nodes[tierName] = node
		log.Printf("created tier %s (0)", tierName)
		for _, con := range tier.Clients {
			host, _, err := net.SplitHostPort(con)
			if err != nil {
				log.Fatalf("%s is not a valid remote host", con)
			}
			log.Printf("creating connection %s:%s", tierName, host)
			connectionHash := fmt.Sprintf("%s:%s", tierName, host)
			connection := &VizceralConnection{}
			connection.Source = tierName
			connection.Target = host
			v.ConnectionMap.connections[connectionHash] = connection
		}
	}
}

func (v *Vizceral) snapshotLoop() {
	for {
		time.Sleep(time.Minute)
		volume := 0
		for _, con := range v.ConnectionMap.connections {
			// There is a race condition here that the original
			// connection object may receive some new observations
			// before we create a new metric instance, and therefore
			// we might lose a few observations. To avoid, a mutex is used
			// but I know that's not very "go like"
			// TODO: use channels for concurrency
			mutex.Lock()
			con.Metrics = con.shadowMetrics
			con.shadowMetrics = Metrics{}
			mutex.Unlock()

			volume += con.Metrics.Sum()
		}
		v.MaxVolume = volume

		now := int32(time.Now().Unix())
		v.Updated = now
		for _, node := range v.NodeMap.nodes {
			node.Updated = now
		}
		log.Printf("took a snapshot with total volume = %d", volume)
	}
}

func logFailedConnection(w http.ResponseWriter, r *http.Request) {
	connection := r.URL.Path[12:]
	if con, ok := vizceral.ConnectionMap.connections[connection]; ok {
		mutex.Lock()
		con.shadowMetrics.Danger++
		mutex.Unlock()
	} else {
		log.Printf("did not find connection: %s", connection)
		w.WriteHeader(http.StatusNotAcceptable)
	}
}

func logCompletedConnection(w http.ResponseWriter, r *http.Request) {
	connection := r.URL.Path[14:]
	connection = strings.Trim(connection, "\n")
	if con, ok := vizceral.ConnectionMap.connections[connection]; ok {
		mutex.Lock()
		con.shadowMetrics.Normal++
		mutex.Unlock()
	} else {
		log.Printf("did not find connection: %s", connection)
		w.WriteHeader(http.StatusNotAcceptable)
	}
}

func get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(vizceral)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("500 - failed to convert vizceral data into JSON"))
		return
	}
}

// Ship holds one tiers in/out config
type Ship struct {
	Replicas int      `yaml:"replicas"`
	Clients  []string `yaml:"clients"`
	Servers  []int    `yaml:"servers"`
}

// Config holds the traffic generator settings
type Config struct {
	Ships map[string]Ship `yaml:"ships"`
}

func (c *Config) getConfig() *Config {

	yamlFile, err := ioutil.ReadFile("conf.yaml")
	if err != nil {
		log.Printf("error opening #%v ", err)
		yamlFile, err = ioutil.ReadFile("/etc/cargo/conf.yaml")
		if err != nil {
			log.Printf("error opening #%v ", err)
		}
	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		log.Fatalf("Unmarshal: %v", err)
	}

	fmt.Printf("Initialized with config = \n\n%s\n\n", yamlFile)
	time.Sleep(2 * time.Second)

	return c
}

// MarshalJSON flattens this map into an array
func (nodes VizceralConnections) MarshalJSON() (resp []byte, err error) {
	var listOfNodes []*VizceralConnection

	for _, con := range nodes.connections {
		listOfNodes = append(listOfNodes, con)
	}

	return json.Marshal(listOfNodes)
}

// MarshalJSON flattens this map into an array
func (nodes VizceralNodes) MarshalJSON() (resp []byte, err error) {
	var listOfNodes []*VizceralNode

	for _, node := range nodes.nodes {
		listOfNodes = append(listOfNodes, node)
	}

	return json.Marshal(listOfNodes)
}
