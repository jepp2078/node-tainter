package main

import (
	"fmt"
	"time"

	log "github.com/Sirupsen/logrus"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	coreV1 "k8s.io/api/core/v1"
	nodeUtil "k8s.io/kubernetes/pkg/api/v1/node"
)
var dmzTaint = &coreV1.Taint{
	Key:    "dedicated",
	Value:  "dmz",
	Effect: coreV1.TaintEffectNoExecute,
}

// Controller struct defines how a controller should encapsulate
// logging, client connectivity, informing (list and watching)
// queueing, and handling of resource changes
type Controller struct {
	logger    *log.Entry
	clientset kubernetes.Interface
	queue     workqueue.RateLimitingInterface
	informer  cache.SharedIndexInformer
}

// Run is the main path of execution for the controller loop
func (c *Controller) Run(stopCh <-chan struct{}) {

	// handle a panic with logging and exiting
	defer utilruntime.HandleCrash()
	// ignore new items in the queue but when all goroutines
	// have completed existing items then shutdown
	defer c.queue.ShutDown()

	// run the informer to start listing and watching resources
	go c.informer.Run(stopCh)

	// do the initial synchronization (one time) to populate resources
	if !cache.WaitForCacheSync(stopCh, c.HasSynced) {
		utilruntime.HandleError(fmt.Errorf("error syncing cache"))
		return
	}

	// run the runWorker method every second with a stop channel
	wait.Until(c.runWorker, time.Second, stopCh)
}

// HasSynced allows us to satisfy the Controller interface
// by wiring up the informer's HasSynced method to it
func (c *Controller) HasSynced() bool {
	return c.informer.HasSynced()
}

// runWorker executes the loop to process new items added to the queue
func (c *Controller) runWorker() {
	// invoke processNextItem to fetch and consume the next change
	// to a watched or listed resource
	for c.processNextItem() {
	}
}

// processNextItem retrieves each queued item and takes the
// necessary handler action based off of if the item was
// created or deleted
func (c *Controller) processNextItem() bool {
	// fetch the next item (blocking) from the queue to process or
	// if a shutdown is requested then return out of this to stop
	// processing
	key, quit := c.queue.Get()

	// stop the worker loop from running as this indicates we
	// have sent a shutdown message that the queue has indicated
	// from the Get method
	if quit {
		return false
	}

	defer c.queue.Done(key)

	// assert the string out of the key (format `namespace/name`)
	keyRaw := key.(string)

	// take the string key and get the object out of the indexer
	//
	// item will contain the complex object for the resource and
	// exists is a bool that'll indicate whether or not the
	// resource was created (true) or deleted (false)
	//
	// if there is an error in getting the key from the index
	// then we want to retry this particular queue key a certain
	// number of times (5 here) before we forget the queue key
	// and throw an error
	item, exists, err := c.informer.GetIndexer().GetByKey(keyRaw)
	if err != nil {
		if c.queue.NumRequeues(key) < 5 {
			c.logger.Errorf("Controller.processNextItem: Failed processing item with key %s with error %v, retrying", key, err)
			c.queue.AddRateLimited(key)
		} else {
			c.logger.Errorf("Controller.processNextItem: Failed processing item with key %s with error %v, no more retries", key, err)
			c.queue.Forget(key)
			utilruntime.HandleError(err)
		}
	}

	// if the item doesn't exist then it was deleted and we need to fire off the handler's
	// ObjectDeleted method. but if the object does exist that indicates that the object
	// was created (or updated) so run the ObjectCreated method
	//
	// after both instances, we want to forget the key from the queue, as this indicates
	// a code path of successful queue key processing

	if !exists {
		//Node deleted. Do nothing, taint is gone with the node.
	} else {
		node := item.(*coreV1.Node)
		if nodeUtil.IsNodeReady(node) {
			if node.Labels["node-role.kubernetes.io/dmz-worker"] == "true" {
					hasTaint := false
					for i := range node.Spec.Taints {
						hasTaint = c.CheckDMZTaint(node.Spec.Taints[i])
					}
					if hasTaint {
						log.Infof("Taint already present on node: %s", key)
						c.queue.Forget(key)
					}else{
						log.Infof("Attempting to taint node: %s", key)
						c.TaintDMZNode(key, node)
					}
			} else {
				log.Infof("DMZ label not present on node: %s", key)
				c.queue.Forget(key)
			}
		} else {
			log.Infof("Node not ready. Re-queuing node: %s", key)
			c.queue.AddAfter(key, 10*time.Second)
		}
	}

	// keep the worker loop running by returning true
	return true
}

//Taints the node with the dmz specific taints.
func (c *Controller) TaintDMZNode(key interface{}, node *coreV1.Node) {
	nodeCopy := node.DeepCopy()

	nodeCopy.Spec.Taints = append(nodeCopy.Spec.Taints, *dmzTaint)
	_, err := c.clientset.CoreV1().Nodes().Update(nodeCopy)
	if err != nil {
		log.Infof("Failed tainting node: %s with error:", key, err)
		c.queue.AddAfter(key, 10*time.Second)
	}else{
		log.Infof("Tainted node: %s", key)
		c.queue.Forget(key)
	}
}

func (c *Controller) CheckDMZTaint(taint coreV1.Taint) bool{
	if taint.MatchTaint(dmzTaint){
		return true
	}
	return false
}

