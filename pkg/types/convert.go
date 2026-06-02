package types

import (
	"time"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func WorkloadToProto(w Workload) *gen.Workload {
	pw := &gen.Workload{
		Id:        w.ID,
		Phase:     gen.WorkloadPhase(w.Phase),
		NodeId:    w.NodeID,
		CreatedAt: w.CreatedAt.Unix(),
	}
	if w.Container != nil {
		pw.Spec = &gen.Workload_Container{Container: ContainerSpecToProto(*w.Container)}
	} else if w.Stack != nil {
		pw.Spec = &gen.Workload_Stack{Stack: ComposeStackSpecToProto(*w.Stack)}
	}
	for _, pa := range w.PortAllocations {
		pw.PortAllocations = append(pw.PortAllocations, &gen.PortAllocation{
			ContainerPort: pa.ContainerPort,
			AllocatedPort: pa.AllocatedPort,
			Protocol:      pa.Protocol,
		})
	}
	return pw
}

func WorkloadFromProto(pw *gen.Workload) Workload {
	w := Workload{
		ID:        pw.Id,
		Phase:     WorkloadPhase(pw.Phase),
		NodeID:    pw.NodeId,
		CreatedAt: time.Unix(pw.CreatedAt, 0),
	}
	switch s := pw.Spec.(type) {
	case *gen.Workload_Container:
		spec := ContainerSpecFromProto(s.Container)
		w.Kind = KindContainer
		w.Container = &spec
	case *gen.Workload_Stack:
		spec := ComposeStackSpecFromProto(s.Stack)
		w.Kind = KindStack
		w.Stack = &spec
	}
	for _, pa := range pw.PortAllocations {
		w.PortAllocations = append(w.PortAllocations, PortAllocation{
			ContainerPort: pa.ContainerPort,
			AllocatedPort: pa.AllocatedPort,
			Protocol:      pa.Protocol,
		})
	}
	return w
}

func ContainerSpecToProto(s ContainerSpec) *gen.ContainerSpec {
	ports := make([]*gen.PortMapping, len(s.Ports))
	for i, p := range s.Ports {
		ports[i] = &gen.PortMapping{
			HostPort:      p.HostPort,
			ContainerPort: p.ContainerPort,
			Protocol:      p.Protocol,
		}
	}
	vols := make([]*gen.VolumeMount, len(s.Volumes))
	for i, v := range s.Volumes {
		vols[i] = &gen.VolumeMount{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly}
	}
	return &gen.ContainerSpec{
		Name:      s.Name,
		Image:     s.Image,
		Command:   s.Command,
		Env:       s.Env,
		Ports:     ports,
		Volumes:   vols,
		Labels:    s.Labels,
		Namespace: s.Namespace,
	}
}

func ContainerSpecFromProto(ps *gen.ContainerSpec) ContainerSpec {
	ports := make([]PortMapping, len(ps.Ports))
	for i, p := range ps.Ports {
		ports[i] = PortMapping{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol}
	}
	vols := make([]VolumeMount, len(ps.Volumes))
	for i, v := range ps.Volumes {
		vols[i] = VolumeMount{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly}
	}
	return ContainerSpec{
		Name:      ps.Name,
		Image:     ps.Image,
		Command:   ps.Command,
		Env:       ps.Env,
		Ports:     ports,
		Volumes:   vols,
		Labels:    ps.Labels,
		Namespace: ps.Namespace,
	}
}

func ComposeStackSpecToProto(s ComposeStackSpec) *gen.ComposeStackSpec {
	return &gen.ComposeStackSpec{Name: s.Name, ComposeYaml: s.ComposeYAML}
}

func ComposeStackSpecFromProto(ps *gen.ComposeStackSpec) ComposeStackSpec {
	return ComposeStackSpec{Name: ps.Name, ComposeYAML: ps.ComposeYaml}
}

func NodeToProto(n Node) *gen.Node {
	return &gen.Node{
		NodeId:  n.ID,
		Address: n.Address,
		Status:  gen.NodeStatus(n.Status),
		Resources: &gen.NodeResources{
			CpuCores:    n.Resources.CPUCores,
			MemoryBytes: n.Resources.MemoryBytes,
			DiskBytes:   n.Resources.DiskBytes,
		},
		LastSeenAt: n.LastSeenAt.Unix(),
		TlsCert:    n.TLSCert,
		DataIp:     n.DataIP,
	}
}

func NodeFromProto(pn *gen.Node) Node {
	n := Node{
		ID:         pn.NodeId,
		Address:    pn.Address,
		Status:     NodeStatus(pn.Status),
		LastSeenAt: time.Unix(pn.LastSeenAt, 0),
		TLSCert:    pn.TlsCert,
		DataIP:     pn.DataIp,
	}
	if pn.Resources != nil {
		n.Resources = NodeResources{
			CPUCores:    pn.Resources.CpuCores,
			MemoryBytes: pn.Resources.MemoryBytes,
			DiskBytes:   pn.Resources.DiskBytes,
		}
	}
	return n
}

func ServiceToProto(s Service) *gen.Service {
	return &gen.Service{
		Id:           s.ID,
		Name:         s.Name,
		WorkloadName: s.WorkloadName,
		TargetPort:   s.TargetPort,
		SystemPort:   s.SystemPort,
		CreatedAt:    s.CreatedAt.Unix(),
	}
}

func ServiceFromProto(ps *gen.Service) Service {
	return Service{
		ID:           ps.Id,
		Name:         ps.Name,
		WorkloadName: ps.WorkloadName,
		TargetPort:   ps.TargetPort,
		SystemPort:   ps.SystemPort,
		CreatedAt:    time.Unix(ps.CreatedAt, 0),
	}
}

func IngressRuleToProto(r IngressRule) *gen.IngressRule {
	return &gen.IngressRule{
		Id:          r.ID,
		Host:        r.Host,
		PathPrefix:  r.PathPrefix,
		ServiceName: r.ServiceName,
		CreatedAt:   r.CreatedAt.Unix(),
	}
}

func IngressRuleFromProto(pr *gen.IngressRule) IngressRule {
	return IngressRule{
		ID:          pr.Id,
		Host:        pr.Host,
		PathPrefix:  pr.PathPrefix,
		ServiceName: pr.ServiceName,
		CreatedAt:   time.Unix(pr.CreatedAt, 0),
	}
}

func ActualWorkloadStateToProto(s ActualWorkloadState) *gen.ActualWorkloadState {
	containers := make([]*gen.ActualContainer, len(s.Containers))
	for i, c := range s.Containers {
		containers[i] = &gen.ActualContainer{
			WorkloadId:  c.WorkloadID,
			ContainerId: c.ContainerID,
			Name:        c.Name,
			Status:      c.Status,
			StartedAt:   c.StartedAt.Unix(),
		}
	}
	stacks := make([]*gen.ActualStack, len(s.Stacks))
	for i, st := range s.Stacks {
		svcs := make([]*gen.ActualContainer, len(st.Services))
		for j, svc := range st.Services {
			svcs[j] = &gen.ActualContainer{
				WorkloadId:  svc.WorkloadID,
				ContainerId: svc.ContainerID,
				Name:        svc.Name,
				Status:      svc.Status,
				StartedAt:   svc.StartedAt.Unix(),
			}
		}
		stacks[i] = &gen.ActualStack{WorkloadId: st.WorkloadID, Name: st.Name, Services: svcs}
	}
	return &gen.ActualWorkloadState{
		NodeId:     s.NodeID,
		Containers: containers,
		Stacks:     stacks,
		ReportedAt: s.ReportedAt.Unix(),
	}
}

func ActualWorkloadStateFromProto(ps *gen.ActualWorkloadState) ActualWorkloadState {
	containers := make([]ActualContainer, len(ps.Containers))
	for i, c := range ps.Containers {
		containers[i] = ActualContainer{
			WorkloadID:  c.WorkloadId,
			ContainerID: c.ContainerId,
			Name:        c.Name,
			Status:      c.Status,
			StartedAt:   time.Unix(c.StartedAt, 0),
		}
	}
	stacks := make([]ActualStack, len(ps.Stacks))
	for i, st := range ps.Stacks {
		svcs := make([]ActualContainer, len(st.Services))
		for j, svc := range st.Services {
			svcs[j] = ActualContainer{
				WorkloadID:  svc.WorkloadId,
				ContainerID: svc.ContainerId,
				Name:        svc.Name,
				Status:      svc.Status,
				StartedAt:   time.Unix(svc.StartedAt, 0),
			}
		}
		stacks[i] = ActualStack{WorkloadID: st.WorkloadId, Name: st.Name, Services: svcs}
	}
	return ActualWorkloadState{
		NodeID:     ps.NodeId,
		Containers: containers,
		Stacks:     stacks,
		ReportedAt: time.Unix(ps.ReportedAt, 0),
	}
}
