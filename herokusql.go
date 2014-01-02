// TODO: Support setting regions
// TODO: Implement deprovision and plan change

package herokusql

import (
	"appengine"
	"appengine/urlfetch"

	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"strings"
)

const (
	password    = "ka5E2i5W50WgYzg9yfc7"
	projectName = "herokusql"
	authScope   = "https://www.googleapis.com/auth/sqlservice.admin"
	insertURL   = "https://www.googleapis.com/sql/v1beta3/projects/" + projectName + "/instances"
	bufSize     = 1 << 13 // 8KB
	basePath    = "/heroku/resource"
)

var instanceExists = errors.New("App already provisioned")
var tierMap = map[string]string{
	"trickle": "D0",
	"stream":  "D1",
	"river":   "D4",
	"deluge":  "D16",
	"torrent": "D32",
}

func init() {
	http.HandleFunc(basePath, handler)
}

func handler(w http.ResponseWriter, r *http.Request) {
	if p, set := r.URL.User.Password(); !set {
		http.Error(w, "Unauthenticated", http.StatusUnauthorized)
		return
	} else if p != password {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if r.URL.Path == basePath { 
		provision(w, r)
	}
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

	w.Header().Add("Content-Type", "application/json")
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
	apiReq.Header.Add("Authorization", "Bearer "+tok)
	buf := bytes.NewBuffer(make([]byte, 0, bufSize))
	json.NewEncoder(buf).Encode(apiRequest{
		Instance: instanceName,
		Project:  projectName,
		Settings: apiRequestSettings{
			Tier:                      tier,
			ActivationPolicy:          "ON_DEMAND",
			AuthorizedGAEApplications: []string{appengine.AppID(c)},
			PricingPlan:               "PACKAGE",
			ReplicationType:           "ASYNCHRONOUS",
		},
	})
	apiReq.Body = ioutil.NopCloser(buf)
	apiHttpResp, err := urlfetch.Client(c).Do(apiReq)
	if apiHttpResp.StatusCode == http.StatusConflict {
		return nil, instanceExists
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
	Tier                      string   `json:"tier"`
	ActivationPolicy          string   `json:"activationPolicy"`
	AuthorizedGAEApplications []string `json:"authorizedGaeApplications"`
	PricingPlan               string   `json:"pricingPlan"`
	ReplicationType           string   `json:"replicationType"`
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
	InstanceURL string `json:"INSTANCE_URL"`
}
