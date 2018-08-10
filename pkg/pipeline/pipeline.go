package pipeline

import (
	"fmt"
	"plugin"

	"github.com/whiteboxio/flow/pkg/config"
	"github.com/whiteboxio/flow/pkg/core"
	buffer "github.com/whiteboxio/flow/pkg/links/buffer"
	dmx "github.com/whiteboxio/flow/pkg/links/dmx"
	fanout "github.com/whiteboxio/flow/pkg/links/fanout"
	mpx "github.com/whiteboxio/flow/pkg/links/mpx"
	router "github.com/whiteboxio/flow/pkg/links/router"
	throttler "github.com/whiteboxio/flow/pkg/links/throttler"
	http_rcv "github.com/whiteboxio/flow/pkg/receiver/http"
	tcp_rcv "github.com/whiteboxio/flow/pkg/receiver/tcp"
	udp_rcv "github.com/whiteboxio/flow/pkg/receiver/udp"
	udp_sink "github.com/whiteboxio/flow/pkg/receiver/udp"
	unix_rcv "github.com/whiteboxio/flow/pkg/receiver/unix"
	dumper_sink "github.com/whiteboxio/flow/pkg/sink/dumper"
	tcp_sink "github.com/whiteboxio/flow/pkg/sink/tcp"
)

type Pipeline struct {
	pplCfg   map[string]config.CfgBlockPipeline
	compsCfg map[string]config.CfgBlockComponent
	compTree *util.NTree
}

type ConstrFunc func(string, core.Params) (core.Link, error)

var (
	compBuilders = map[string]ConstrFunc{
		"receiver.tcp":    tcp_rcv.NewTCP,
		"receiver.udp":    udp_rcv.NewUDP,
		"receiver.http":   http_rcv.NewHTTP,
		"receiver.unix":   unix_rcv.NewUnix,
		"links.dmx":       dmx.NewDMX,
		"links.mpx":       mpx.NewMPX,
		"links.router":    router.NewRouter,
		"links.throttler": throttler.NewThrottler,
		"links.fanout":    fanout.NewFanout,
		"links.buffer":    buffer.NewBuffer,
		"sink.dumper":     dumper_sink.NewDumper,
		"sink.tcp":        tcp_sink.NewTCP,
		"sink.udp":        udp_sink.NewUDP,
	}
)

func NewPipeline(comps map[string]config.CfgBlockComponent,
	ppl map[string]config.CfgBlockPipeline) (*Pipeline, error) {

	compPool := make(map[string]core.Link)
	for compName, compParams := range comps {
		comp, compErr := buildComp(compName, compParams)
		if compErr != nil {
			return nil, compErr
		}
		if _, ok := compPool[compName]; ok {
			return nil,
				fmt.Errorf(
					"Duplicate declaration of component called %s", compName)
		}
		compPool[compName] = comp
	}

	for compName, compCfg := range ppl {
		comp, ok := compPool[compName]
		if !ok {
			return nil, fmt.Errorf(
				"Undefined component %s in the pipeline", compName)
		}
		if compCfg.Connect != "" {
			tell.Infof("Connecting %s to %s", compName, compCfg.Connect)
			if _, ok := compPool[compCfg.Connect]; !ok {
				return nil, fmt.Errorf(
					"Failed to connect %s to %s: %s is undefined",
					compName, compCfg.Connect, compCfg.Connect)
			}
			if err := comp.ConnectTo(compPool[compCfg.Connect]); err != nil {
				return nil, fmt.Errorf("Failed to connect %s to %s: %s",
					compName, compCfg.Connect, err.Error())
			}
		}
		if len(compCfg.Links) > 0 {
			tell.Infof("Linking %s with %s", compName, compCfg.Links)
			links := make([]flow.Link, len(compCfg.Links))
			for ix, linkName := range compCfg.Links {
				if _, ok := compPool[linkName]; !ok {
					return nil, fmt.Errorf(
						"Failed to link %s to %s: %s is undefined",
						compName, linkName, linkName)
				}
				links[ix] = compPool[linkName]
			}
			if err := comp.LinkTo(links); err != nil {
				return nil, fmt.Errorf(
					"Failed to link %s: %s", compName, err.Error())
			}
		}
		if len(compCfg.Routes) > 0 {
			routes := make(map[string]flow.Link)
			for rtPath, rtName := range compCfg.Routes {
				if _, ok := compPool[rtName]; !ok {
					return nil, fmt.Errorf(
						"Failed to route %s to %s under path %s: %s is undefined",
						compName, rtName, rtPath, rtName)
				}
				routes[rtPath] = compPool[rtName]
			}
			if err := comp.RouteTo(routes); err != nil {
				return nil, fmt.Errorf("Failed to route %s: %s",
					compName, err.Error())
			}
		}
	}

	pipeline := &Pipeline{
		pplCfg:   ppl,
		compsCfg: comps,
		compTree: buildCompTree(ppl, compPool),
	}

	return pipeline, nil
}

func buildComp(compName string, cfg config.CfgBlockComponent) (flow.Link, error) {
	if cfg.Plugin != "" {
		pluginPath, _ := config.Get("flow.plugin.path")
		if pluginPath.(string) == "" {
			pluginPath = "/etc/msgrelay/plugins"
		}
		p, pErr := plugin.Open(fmt.Sprintf("%s/%s/%s.so",
			pluginPath.(string), cfg.Plugin, cfg.Plugin))
		if pErr != nil {
			return nil, pErr
		}
		c, cErr := p.Lookup(cfg.Constructor)
		if cErr != nil {
			return nil, cErr
		}
		comp, err := c.(func(string, flow.Params) (flow.Link, error))(compName, cfg.Params)
		if err != nil {
			panic(err.Error())
		}
		if comp == nil {
			panic("Component is nil")
		}
		fmt.Printf("Pipeline sees the link as: %+v\n", comp)
		return comp, err
	} else {
		if builder, ok := compBuilders[cfg.Module]; ok {
			return builder(compName, cfg.Params)
		}
		return nil, fmt.Errorf("Unknown module: %s requested by %s", cfg.Module, compName)
	}
}

func (ppl *Pipeline) Explain() string {
	relTmpl := "    %s -> %s\n"
	relLblTmpl := "    %s -> %s [label=\"%s\"]\n"
	graphViz := "digraph G {\n"
	for compName, compCfg := range ppl.pplCfg {
		if len(compCfg.Connect) != 0 {
			graphViz += fmt.Sprintf(relTmpl, compName, compCfg.Connect)
		}
		if len(compCfg.Links) != 0 {
			for _, link := range compCfg.Links {
				if len(compCfg.Connect) > 0 {
					graphViz += fmt.Sprintf(relTmpl, link, compName)
				} else {
					graphViz += fmt.Sprintf(relTmpl, compName, link)
				}
			}
		}
		if len(compCfg.Routes) != 0 {
			for key, route := range compCfg.Routes {
				graphViz += fmt.Sprintf(relLblTmpl, compName, route, key)
			}
		}
	}

	graphViz += "}"

	return graphViz
}

func (ppl *Pipeline) ExecCmd(cmd *flow.Cmd, cmdPpgt flow.CmdPropagation) error {
	var stack []interface{}
	switch cmdPpgt {
	case flow.CmdPpgtBtmUp:
		stack = ppl.compTree.PostTraversal()
	case flow.CmdPpgtTopDwn:
		stack = ppl.compTree.PreTraversal()
	default:
		panic("This should not happen, OlegS made a mistake")
	}
	for _, link := range stack {
		if err := link.(flow.Link).ExecCmd(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (ppl *Pipeline) Start() error {
	return ppl.ExecCmd(&flow.Cmd{Code: flow.CmdCodeStart}, flow.CmdPpgtBtmUp)
}

func (ppl *Pipeline) Stop() error {
	return ppl.ExecCmd(&flow.Cmd{Code: flow.CmdCodeStop}, flow.CmdPpgtTopDwn)
}

func buildCompTree(ppl map[string]config.CfgBlockPipeline,
	lookup map[string]flow.Link) *util.NTree {

	rootNode := &util.NTree{}

	for name, block := range ppl {
		ptr := rootNode.FindOrInsert(lookup[name])
		children := make([]flow.Link, 0)
		if len(block.Connect) > 0 {
			children = append(children, lookup[block.Connect])
		}
		if len(block.Links) > 0 {
			for _, linkName := range block.Links {
				children = append(children, lookup[linkName])
			}
		}
		if len(block.Routes) > 0 {
			for _, routeName := range block.Routes {
				children = append(children, lookup[routeName])
			}
		}
		for _, chld := range children {
			if chldPtr := rootNode.Detach(chld); chldPtr != nil {
				ptr.FindOrInsert(chldPtr.GetValue())
			}
			ptr.FindOrInsert(chld)
		}
	}

	return rootNode
}