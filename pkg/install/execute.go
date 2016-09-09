package install

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/apprenda/kismatic-platform/pkg/ansible"
)

// The Executor will carry out the installation plan
type Executor interface {
	Install(p *Plan) error
}

type ansibleExecutor struct {
	runner          ansible.Runner
	tlsDirectory    string
	restartServices bool
}

// NewExecutor returns an executor for performing installations according to the installation plan.
func NewExecutor(out io.Writer, errOut io.Writer, tlsDirectory string, restartServices bool) (Executor, error) {
	r, w := io.Pipe()

	// Send runner output to the pipe
	ansibleDir := "ansible" // TODO: Is there a better way to handle this path to the ansible install?
	runner, err := ansible.NewRunner(w, errOut, ansibleDir)
	if err != nil {
		return nil, fmt.Errorf("error creating ansible runner: %v", err)
	}

	// Pipe runner output into parser
	parser := &ansible.OutputParser{Out: out}
	go parser.Transform(r)

	td, err := filepath.Abs(tlsDirectory)
	if err != nil {
		return nil, fmt.Errorf("error getting absolute path from %q: %v", tlsDirectory, err)
	}

	return &ansibleExecutor{
		runner:          runner,
		tlsDirectory:    td,
		restartServices: restartServices,
	}, nil
}

// Install the cluster according to the installation plan
func (e *ansibleExecutor) Install(p *Plan) error {
	// Build the ansible inventory
	etcdNodes := []ansible.Node{}
	for _, n := range p.Etcd.Nodes {
		etcdNodes = append(etcdNodes, installNodeToAnsibleNode(&n, &p.Cluster.SSH))
	}
	masterNodes := []ansible.Node{}
	for _, n := range p.Master.Nodes {
		masterNodes = append(masterNodes, installNodeToAnsibleNode(&n, &p.Cluster.SSH))
	}
	workerNodes := []ansible.Node{}
	for _, n := range p.Worker.Nodes {
		workerNodes = append(workerNodes, installNodeToAnsibleNode(&n, &p.Cluster.SSH))
	}
	inventory := ansible.Inventory{
		{
			Name:  "etcd",
			Nodes: etcdNodes,
		},
		{
			Name:  "master",
			Nodes: masterNodes,
		},
		{
			Name:  "worker",
			Nodes: workerNodes,
		},
	}

	dnsIP, err := getDNSServiceIP(p)
	if err != nil {
		return fmt.Errorf("error getting DNS service IP: %v", err)
	}

	ev := ansible.ExtraVars{
		"kubernetes_cluster_name":   p.Cluster.Name,
		"kubernetes_admin_password": p.Cluster.AdminPassword,
		"tls_directory":             e.tlsDirectory,
		"calico_network_type":       p.Cluster.Networking.Type,
		"kubernetes_services_cidr":  p.Cluster.Networking.ServiceCIDRBlock,
		"kubernetes_pods_cidr":      p.Cluster.Networking.PodCIDRBlock,
		"kubernetes_dns_service_ip": dnsIP,
	}

	if p.Cluster.LocalRepository != "" {
		ev["local_repoository_path"] = p.Cluster.LocalRepository
	}

	if e.restartServices {
		services := []string{"etcd", "apiserver", "controller", "scheduler", "proxy", "kubelet", "calico_node", "docker"}
		for _, s := range services {
			ev[fmt.Sprintf("force_%s_restart", s)] = strconv.FormatBool(true)
		}
	}

	// Run the installation playbook
	err = e.runner.RunPlaybook(inventory, "kubernetes.yaml", ev)
	if err != nil {
		return fmt.Errorf("error running ansible playbook: %v", err)
	}
	return nil
}

// Converts plan node to ansible node
func installNodeToAnsibleNode(n *Node, s *SSHConfig) ansible.Node {
	return ansible.Node{
		Host:          n.Host,
		PublicIP:      n.IP,
		InternalIP:    n.InternalIP,
		SSHPrivateKey: s.Key,
		SSHUser:       s.User,
		SSHPort:       s.Port,
	}
}
