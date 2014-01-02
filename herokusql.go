// TODO: Support setting regions
// TODO: Implement deprovision and plan change

package herokusql

import (
	"appengine"
	"appengine/urlfetch"

	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"strings"
)

const (
	password    = "f1937226ef79503baddc427190207e5d"
	projectName = "herokusql"
	authScope   = "https://www.googleapis.com/auth/sqlservice.admin"
	insertURL   = "https://www.googleapis.com/sql/v1beta3/projects/" + projectName + "/instances"
	bufSize     = 1 << 13 // 8KB
	basePath    = "/heroku/resources"
)

var instanceExists = errors.New("App already provisioned")
var tierMap = map[string]string{
	"trickle": "D0",
	"stream":  "D1",
	"river":   "D4",
	"deluge":  "D16",
	"torrent": "D32",

	// For testing
	"test": "D0",
}

func init() {
	http.HandleFunc(basePath, handler)
}

func handler(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.URL.Path == basePath && r.Method == "POST" {
		provision(w, r)
	}
}

func checkAuth(r *http.Request) bool {
	s := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(s) != 2 || s[0] != "Basic" {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return false
	}
	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return false
	}

	if pair[1] != password {
		return false
	}
	return true
}

func provision(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	defer r.Body.Close()
	var herReq herokuRequest
	if err := json.NewDecoder(r.Body).Decode(&herReq); err != nil {
		c.Errorf("decode: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	instanceName := herReq.HerokuID[:strings.Index(herReq.HerokuID, "@")]
	tier, ok := tierMap[herReq.Plan]
	if !ok {
		c.Errorf("tier: %s", herReq.Plan)
		http.Error(w, "Invalid plan "+herReq.Plan, http.StatusBadRequest)
		return
	}

	apiResp, err := apiInsert(c, instanceName, tier)
	if err == instanceExists {
		http.Error(w, "App is already provisioned", http.StatusConflict)
		return
	} else if err != nil {
		http.Error(w, "Error creating instance", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(herokuResponse{
		ID: instanceName,
		Config: herokuResponseConfig{
			InstanceURL: apiResp.IPAddresses[0].IPAddress,
		},
		Message: "Provision successful!",
	})
}

func apiInsert(c appengine.Context, instanceName, tier string) (*apiResponse, error) {
	apiReq, err := http.NewRequest("POST", insertURL, nil)
	if err != nil {
		c.Errorf("new request: %v", err)
		return nil, err
	}
	tok, _, err := appengine.AccessToken(c, authScope)
	if err != nil {
		c.Errorf("accesstoken: %v", err)
		return nil, err
	}
	apiReq.Header.Set("Authorization", "Bearer "+tok)
	apiReq.Header.Set("Content-Type", "application/json")
	buf := bytes.NewBuffer(make([]byte, 0, bufSize))
	json.NewEncoder(buf).Encode(apiRequest{
		Instance: instanceName,
		Project:  projectName,
		Settings: apiRequestSettings{
			Tier:                      tier,
			ActivationPolicy:          "ON_DEMAND",
			AuthorizedGAEApplications: []string{appengine.AppID(c)},
			PricingPlan:               "PER_USE",
			ReplicationType:           "ASYNCHRONOUS",
			IPConfiguration: apiRequestIPConfig{
				Enabled: true,
			},
		},
	})
	apiReq.Body = ioutil.NopCloser(buf)
	apiHttpResp, err := urlfetch.Client(c).Do(apiReq)
	if apiHttpResp.StatusCode == http.StatusConflict {
		return nil, instanceExists
	} else if apiHttpResp.StatusCode != http.StatusOK {
		slurp, _ := ioutil.ReadAll(apiHttpResp.Body)
		c.Errorf("API request failed:\n %d, %s", apiHttpResp.StatusCode, slurp)
		return nil, errors.New("insert failed")
	}
	if err != nil {
		c.Errorf("insert: %v", err)
		return nil, err
	}
	var apiResp apiResponse
	defer apiHttpResp.Body.Close()
	if err := json.NewDecoder(apiHttpResp.Body).Decode(&apiResp); err != nil {
		c.Errorf("decode: %v", err)
		return nil, err
	}
	return &apiResp, nil
}

type apiRequest struct {
	Instance string             `json:"instance"`
	Project  string             `json:"project"`
	Settings apiRequestSettings `json:"settings"`
}

type apiRequestSettings struct {
	Tier                      string             `json:"tier"`
	ActivationPolicy          string             `json:"activationPolicy"`
	AuthorizedGAEApplications []string           `json:"authorizedGaeApplications"`
	PricingPlan               string             `json:"pricingPlan"`
	ReplicationType           string             `json:"replicationType"`
	IPConfiguration           apiRequestIPConfig `json:"ipConfiguration"`
}

type apiRequestIPConfig struct {
	Enabled bool `json:"enabled"`
}

type apiResponse struct {
	IPAddresses []struct {
		IPAddress string `json:"ipAddress"`
	} `json:"ipAddresses"`
}

type herokuRequest struct {
	HerokuID    string `json:"heroku_id"`
	Plan        string `json:"plan"`
	CallbackURL string `json:"callback_url"`
}

type herokuResponse struct {
	ID      string               `json:"id"`
	Config  herokuResponseConfig `json:"config"`
	Message string               `json:"message"`
}

type herokuResponseConfig struct {
	InstanceURL string `json:"GOOGLECLOUDSQL_URL"`
}
