package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"

	"github.com/gorilla/mux"
	eventingv1alpha1 "github.com/kyma-project/kyma/components/eventing-controller/api/v1alpha1"
	serverlessv1alpha1 "github.com/kyma-project/kyma/components/function-controller/pkg/apis/serverless/v1alpha1"
	"github.com/vladislavpaskar/hackathon2022/components/backend/clients/forwarder"
	"github.com/vladislavpaskar/hackathon2022/components/backend/clients/function"
	"github.com/vladislavpaskar/hackathon2022/components/backend/clients/subscription"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type K8sResourceClients struct {
	subscriptionClient subscription.Client
	functionClient     function.Client
}

var K8sClients = make(map[string]*K8sResourceClients)
var k8sClientConfigs = make(map[string]*rest.Config)
var kubeconfigs = make(map[string]string)
var defaultCluster = "default"
var portForwardResult *forwarder.Result = nil

type SubscriptionData struct {
	Sink         string `json:"sink"`
	AppName      string `json:"appName"`
	EventName    string `json:"eventName"`
	EventVersion string `json:"eventVersion"`
}

func main() {
	// Start the server
	handleRequests()

	if portForwardResult != nil {
		portForwardResult.Close()
	}
}

func handleRequests() {
	r := mux.NewRouter().StrictSlash(true)
	r.Use(commonMiddleware)

	r.HandleFunc("/api/kubeconfig/{name}", addKubeconfig).Methods("POST")
	r.HandleFunc("/api/kubeconfigs", getKubeconfigs).Methods("GET")

	r.HandleFunc("/api/subs", getAllSubs).Methods("GET")
	r.HandleFunc("/api/{ns}/subs/{name}", postSub).Methods("POST")
	r.HandleFunc("/api/{ns}/subs/{name}", getSub).Methods("GET")
	r.HandleFunc("/api/{ns}/subs/{name}", putSub).Methods("PUT")
	r.HandleFunc("/api/{ns}/subs/{name}", delSub).Methods("DELETE")

	r.HandleFunc("/api/funcs/", getAllFunctions).Methods("GET")
	r.HandleFunc("/api/{ns}/funcs/{name}", postFunction).Methods("POST")
	r.HandleFunc("/api/{ns}/funcs/{name}", getFunction).Methods("GET")
	r.HandleFunc("/api/{ns}/funcs/{name}", putFunction).Methods("PUT")
	r.HandleFunc("/api/{ns}/funcs/{name}", delFunction).Methods("DELETE")
	r.HandleFunc("/api/{ns}/funcs/{name}/logs", getFunctionLogs).Methods("GET")

	r.HandleFunc("/api/publishEvent", publishEvent).Methods("POST")

	r.HandleFunc("/api/cleaneventtypes", getAllCleanEventTypes).Methods("GET")

	log.Printf("Server listening on port 8000 ...")
	log.Fatal(http.ListenAndServe(":8000", r))
}

func commonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func portForwardEPP() (*forwarder.Result, error) {
	options := []*forwarder.Option{
		{
			// https://github.com/anthhub/forwarder
			// if local port isn't provided, forwarder will generate a random port number
			// if target port isn't provided, forwarder find the first container port of the pod or service
			LocalPort: 9091,
			// the k8s pod port
			RemotePort: 8080,
			// the forwarding service name
			ServiceName: "eventing-publisher-proxy",
			// the k8s source string, eg: svc/my-nginx-svc po/my-nginx-666
			// the Source field will be parsed and override ServiceName or RemotePort field
			//Source: "svc/my-nginx-66b6c48dd5-ttdb2",
			// namespace default is "default"
			Namespace: "kyma-system",
		},
	}

	ret, err := forwarder.Forwarders(context.Background(), options, k8sClientConfigs[defaultCluster])
	if err != nil {
		return nil, err
	}

	//// remember to close the forwarding
	//defer ret.Close()

	// wait forwarding ready
	// the remote and local ports are listed
	ports, err := ret.Ready()
	if err != nil {
		return nil, err
	}

	fmt.Printf("port-forward started to ports: %+v\n", ports)

	return ret, err
}

func getKubeconfigs(w http.ResponseWriter, r *http.Request) {
	keys := reflect.ValueOf(kubeconfigs).MapKeys()

	data, err := json.Marshal(keys)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return response to user
	_, err = w.Write(data)
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func addKubeconfig(w http.ResponseWriter, r *http.Request) {
	// Fetch data from URI
	name := mux.Vars(r)["name"]

	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	kc := string(data)
	if kc == "" {
		log.Printf("%s %s Invalid req body", r.Method, r.RequestURI)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	kubeconfigs[name] = kc

	k8sConfig, err := clientcmd.NewClientConfigFromBytes([]byte(kc))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	clientConfig, err := k8sConfig.ClientConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	k8sClientConfigs[name] = clientConfig
	// Create dynamic client (k8s)
	dynamicClient, err := dynamic.NewForConfig(clientConfig)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// setup clients
	resourceClients := &K8sResourceClients{
		subscriptionClient: subscription.NewClient(dynamicClient),
		functionClient:     function.NewClient(dynamicClient),
	}

	K8sClients[name] = resourceClients

	kubeconfigs[defaultCluster] = kubeconfigs[name]
	k8sClientConfigs[defaultCluster] = k8sClientConfigs[name]
	K8sClients[defaultCluster] = K8sClients[name]

	// start the port-forward to EPP
	portForwardResult, err = portForwardEPP()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	log.Print("kubeconfig was set")
}

func getAllSubs(w http.ResponseWriter, r *http.Request) {
	namespace := "default"
	// Fetch namespace info from the query parameters
	v := r.URL.Query()
	if v.Get("ns") == "-A" {
		namespace = ""
	} else if v.Get("ns") != "" {
		namespace = v.Get("ns")
	}

	// Get subscriptions from the k8s cluster
	subsUnstructured, err := K8sClients[defaultCluster].subscriptionClient.ListJson(namespace)
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Convert response to bytes
	subsBytes, err := subsUnstructured.MarshalJSON()
	if err != nil {
		log.Printf("%s %s failed to marchal json: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return response to user
	_, err = w.Write(subsBytes)
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func getAllCleanEventTypes(w http.ResponseWriter, r *http.Request) {
	namespace := "default"
	// Fetch namespace info from the query parameters
	v := r.URL.Query()
	if v.Get("ns") == "-A" {
		namespace = ""
	} else if v.Get("ns") != "" {
		namespace = v.Get("ns")
	}

	// Get subscriptions from the k8s cluster
	subList, err := K8sClients[defaultCluster].subscriptionClient.List(namespace)
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cleanEventTypes := make([]string, 0)
	for _, sub := range subList.Items {
		if sub.Status.CleanEventTypes == nil {
			continue
		}

		for _, cleanedType := range sub.Status.CleanEventTypes {
			if !contains(cleanEventTypes, cleanedType) {
				cleanEventTypes = append(cleanEventTypes, cleanedType)
			}
		}
	}

	// Convert response to bytes
	data, err := json.Marshal(cleanEventTypes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return response to user
	_, err = w.Write(data)
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func postSub(w http.ResponseWriter, r *http.Request) {
	// Fetch data from URI
	namespace := mux.Vars(r)["ns"]
	name := mux.Vars(r)["name"]

	// Fetch data from request body
	var newSubData SubscriptionData
	err := json.NewDecoder(r.Body).Decode(&newSubData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var eventTypes []string
	newType := fmt.Sprintf("sap.kyma.custom.%s.%s.%s", newSubData.AppName, newSubData.EventName, newSubData.EventVersion)
	eventTypes = append(eventTypes, newType)

	// Initialize a subscription object
	newSub := &eventingv1alpha1.Subscription{
		Spec: eventingv1alpha1.SubscriptionSpec{
			Sink:   newSubData.Sink,
			Filter: &eventingv1alpha1.BEBFilters{},
		},
	}
	newSub.Kind = "Subscription"
	newSub.APIVersion = "eventing.kyma-project.io/v1alpha1"
	newSub.Name = name
	newSub.Namespace = namespace

	for _, eventType := range eventTypes {
		eventFilter := &eventingv1alpha1.BEBFilter{
			EventSource: &eventingv1alpha1.Filter{
				Property: "source",
				Type:     "exact",
				Value:    "",
			},
			EventType: &eventingv1alpha1.Filter{
				Property: "type",
				Type:     "exact",
				Value:    eventType,
			},
		}

		newSub.Spec.Filter.Filters = append(newSub.Spec.Filter.Filters, eventFilter)
	}

	// Create subscription on the k8s cluster
	_, err = K8sClients[defaultCluster].subscriptionClient.CreateSubscription(*newSub)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func getSub(w http.ResponseWriter, r *http.Request) {
	// Fetch data from URI
	namespace := mux.Vars(r)["ns"]
	name := mux.Vars(r)["name"]

	subUnstructured, err := K8sClients[defaultCluster].subscriptionClient.GetSubJson(name, namespace)
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Convert response to bytes
	subsBytes, err := subUnstructured.MarshalJSON()
	if err != nil {
		log.Printf("%s %s failed to marchal json: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return response to user
	_, err = w.Write(subsBytes)
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
	}
}

func putSub(w http.ResponseWriter, r *http.Request) {
	// Fetch data from URI
	name := mux.Vars(r)["name"]
	namespace := mux.Vars(r)["ns"]
	if namespace == "" {
		namespace = "default"
	}

	// Fetch data from request body
	var newSubData SubscriptionData
	err := json.NewDecoder(r.Body).Decode(&newSubData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var eventTypes []string
	newType := fmt.Sprintf("sap.kyma.custom.%s.%s.%s", newSubData.AppName, newSubData.EventName, newSubData.EventVersion)
	eventTypes = append(eventTypes, newType)

	// Initialize a subscription object
	newSub := &eventingv1alpha1.Subscription{
		Spec: eventingv1alpha1.SubscriptionSpec{
			Sink:   newSubData.Sink,
			Filter: &eventingv1alpha1.BEBFilters{},
		},
	}
	newSub.Kind = "Subscription"
	newSub.APIVersion = "eventing.kyma-project.io/v1alpha1"
	newSub.Name = name
	newSub.Namespace = namespace

	for _, eventType := range eventTypes {
		eventFilter := &eventingv1alpha1.BEBFilter{
			EventSource: &eventingv1alpha1.Filter{
				Property: "source",
				Type:     "exact",
				Value:    "",
			},
			EventType: &eventingv1alpha1.Filter{
				Property: "type",
				Type:     "exact",
				Value:    eventType,
			},
		}

		newSub.Spec.Filter.Filters = append(newSub.Spec.Filter.Filters, eventFilter)
	}

	// Create subscription on the k8s cluster
	_, err = K8sClients[defaultCluster].subscriptionClient.UpdateSubscription(*newSub)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func delSub(w http.ResponseWriter, r *http.Request) {
	// Fetch data from URI
	namespace := mux.Vars(r)["ns"]
	name := mux.Vars(r)["name"]

	// check
	// Delete subscription
	err := K8sClients[defaultCluster].subscriptionClient.DeleteSubscription(name, namespace)
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func postFunction(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	namespace := mux.Vars(r)["ns"]
	if namespace == "" {
		namespace = "default"
	}

	// initialize a function object
	var minReplicas int32 = 1
	var maxReplicas int32 = 5
	newFunction := serverlessv1alpha1.Function{
		Spec: serverlessv1alpha1.FunctionSpec{
			MinReplicas: &minReplicas,
			MaxReplicas: &maxReplicas,
		},
	}
	newFunction.Spec.Deps = "{ \n  \"name\": \"test\",\n  \"version\": \"1.0.0\",\n  \"dependencies\":{}\n}"
	newFunction.Spec.Source = "module.exports = {\n main: function (event, context) {\n  console.log(event.data);\n  return \"Hello World!\";\n  }\n}"
	newFunction.Spec.Runtime = serverlessv1alpha1.Nodejs16
	newFunction.APIVersion = "serverless.kyma-project.io/v1alpha1"
	newFunction.Kind = "Function"
	newFunction.Name = name
	newFunction.Namespace = namespace

	_, err := K8sClients[defaultCluster].functionClient.CreateFunction(newFunction)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func getAllFunctions(w http.ResponseWriter, r *http.Request) {
	logHitEndpoint(r.RequestURI)
	namespace := "default"
	// Fetch namespace info from the query parameters
	v := r.URL.Query()
	if v.Get("ns") == "-A" {
		namespace = ""
	} else if v.Get("ns") != "" {
		namespace = v.Get("ns")
	}

	// Get tiny functions from the k8s cluster
	// tiny functions only hold name, namespace and source
	fnBytes, err := K8sClients[defaultCluster].functionClient.MarshaledTinyFunctionList(namespace)
	if err != nil {
		log.Printf("%s %s failed to marchal json: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return response to user
	_, err = w.Write(fnBytes)
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func getFunction(w http.ResponseWriter, r *http.Request) {
	logHitEndpoint(r.RequestURI)
	// Fetch data from URI
	name := mux.Vars(r)["name"]
	namespace := mux.Vars(r)["ns"]
	if namespace == "" {
		namespace = "default"
	}

	fnUnstructured, err := K8sClients[defaultCluster].functionClient.GetFnJson(name, namespace)
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Convert response to bytes
	fnBytes, err := fnUnstructured.MarshalJSON()
	if err != nil {
		log.Printf("%s %s failed to marchal json: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return response to user
	_, err = w.Write(fnBytes)
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
	}
}

func putFunction(w http.ResponseWriter, r *http.Request) {
	logHitEndpoint(r.RequestURI)
	//TODO?
	w.WriteHeader(http.StatusOK)
}

func delFunction(w http.ResponseWriter, r *http.Request) {
	logHitEndpoint(r.RequestURI)
	// Fetch data from URI
	namespace := mux.Vars(r)["ns"]
	name := mux.Vars(r)["name"]
	if namespace == "" {
		namespace = "default"
	}
	// check
	// Delete subscription
	err := K8sClients[defaultCluster].functionClient.DeleteFunction(name, namespace)
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func getFunctionLogs(w http.ResponseWriter, r *http.Request) {
	logHitEndpoint(r.RequestURI)
	// Fetch data from URI
	namespace := mux.Vars(r)["ns"]
	name := mux.Vars(r)["name"]

	// check
	// Delete subscription
	logsData, err := K8sClients[defaultCluster].functionClient.GetFunctionLogs(name, namespace, k8sClientConfigs[defaultCluster])
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var prettyData string
	for _, d := range logsData {
		prettyData = d
	}

	// Convert response to bytes
	data, err := json.Marshal(prettyData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return response to user
	// w.Header().Set("Content-Type", "text/plain")
	_, err = w.Write([]byte(data))
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
	}
}

func publishEvent(w http.ResponseWriter, r *http.Request) {
	// forward the event to EPP
	response, err := forwardEventToEPP(r)
	if err != nil {
		portForwardResult, err = portForwardEPP()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// try again
		response, err = forwardEventToEPP(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(response.StatusCode)
}

func forwardEventToEPP(r *http.Request) (*http.Response, error) {
	// forward the event to EPP
	newRequest, err := http.NewRequest("POST", "http://localhost:9091/publish", r.Body)
	if err != nil {
		return nil, err
	}
	newRequest.Header = r.Header.Clone()

	client := &http.Client{}
	response, err := client.Do(newRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	return response, nil
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}

func logHitEndpoint(endpoint string) {
	log.Printf("hit endpoint %s", endpoint)
}
