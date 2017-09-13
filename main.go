package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/push"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"gopkg.in/yaml.v2"
)

//配置文件yaml
type RConfig struct {
	Gw struct {
		Addr           string `yaml:"addr"`
		HttpListenPort int    `yaml:"httpListenPort"`
		DBaddr         string `yaml:"dbaddr"`
		DBname         string `yaml:"dbname"`
		Tablename      string `yaml:"tablename"`
		Tablename2     string `yaml:"tablename2"`
	}
	Output struct {
		Prometheus      bool   `yaml:"prometheus"`
		PushGateway     bool   `yaml:"pushGateway"`
		PushGatewayAddr string `yaml:"pushGatewayAddr"`
		MonitorID       string `yaml:"monitorID"`
		Period          int    `yaml:"period"`
	}
	P2P struct {
		HistogramOptsparam map[string]float64  `yaml:"histogramOptsparam"`
		SummaryOptsparam   map[float64]float64 `yaml:"summaryOptsparam"`
		Shorttimeedge      int64               `yaml:"shorttimeedge"`
		Longtimeedge       int64               `yaml:"longtimeedge"`
		Gooddelay          float64             `yaml:"gooddelay"`
		Zdelay             float64             `yaml:"zdelay"`
		Goodloss           float64             `yaml:"goodloss"`
		Zloss              float64             `yaml:"zloss"`
	}
}

var globeCfg *RConfig

//mongodb 数据mode
type BaseLog struct {
	Starttime   int64  `bson:"starttime"`
	Endtime     int64  `bson:"endtime"`
	Reporttype  int64  `bson:"reporttype"`
	SidReporter string `bson:"sidReporter"`
	Called      int64  `bson:"called"`
}
type Log struct {
	CallBaseLog map[string]string `bson:"callBaseLog"`
	Sid         string            `bson:"sid"`
}

type itime struct {
	InsertTime int64 `bson:"insertTime"`
}

//规定通话长短类型的时间界限
var shotrtime int64
var longtime int64
var gooddelay float64
var goodloss float64
var zdelay float64
var zloss float64

//HistogramOpt参数
var HistogramOptsparamMap = make(map[string]float64)

//SummaryOpt参数
var SummaryOptsparamMap = make(map[float64]float64)

//上一次的数据库的最后插入时间
var lasttime int64 = 0

//prometheus var
var (
	nodeh *(prometheus.HistogramVec)
	nodes *(prometheus.SummaryVec)
	node  *(prometheus.GaugeVec)
	node2 *(prometheus.GaugeVec)
)

func Observe(vora string, time int64) {

	nodeh.WithLabelValues(vora).Observe(float64(time / 1000))
	nodes.WithLabelValues(vora).Observe(float64(time / 1000))

}
func Observetimeedge(al, ac, as, vl, vc, vs int64) {
	stime := strconv.FormatInt(shotrtime/1000, 10)
	ltime := strconv.FormatInt(longtime/1000, 10)

	node.WithLabelValues("audio", ">"+ltime).Set(float64(al))
	node.WithLabelValues("audio", "<"+stime).Set(float64(as))
	node.WithLabelValues("audio", "<"+ltime+"&&>"+stime).Set(float64(ac))
	node.WithLabelValues("video", ">"+ltime).Set(float64(vl))
	node.WithLabelValues("video", "<"+stime).Set(float64(vs))
	node.WithLabelValues("video", "<"+ltime+"&&>"+stime).Set(float64(vc))

}

//从mongedb获取以解码的callBaseLog数据数组
func mongodbToBaselog(ip string, db string, table string, table2 string) []BaseLog {

	looptime := int64(globeCfg.Output.Period)
	session, err := mgo.Dial(ip)

	if err != nil {
		panic(err)
	}
	defer session.Close()

	collection := session.DB(db).C(table)

	var nowtime itime
	err = collection.Find(bson.M{}).Sort("-insertTime").Limit(1).Select(bson.M{"insertTime": 1}).One(&nowtime)

	var min10time int64
	if lasttime == 0 {
		min10time = nowtime.InsertTime - looptime*1000
	} else {
		min10time = lasttime
	}
	var BaseLogresult []BaseLog

	//通过InsertTime获取日志中通话的开始和结束时间
	err = collection.Find(bson.M{"insertTime": bson.M{"$gte": min10time, "$lt": nowtime.InsertTime}, "isCaller": true}).Select(bson.M{"starttime": 1, "endtime": 1, "reporttype": 1, "sidReporter": 1, "called": 1}).All(&BaseLogresult)
	if err != nil {
		panic(err)
	}
	collection2 := session.DB(db).C(table2)
	var a, b, c int
	for _, v := range BaseLogresult {
		time := v.Endtime - v.Starttime
		if time > 0 && time < 60*60*24*1000 {
			sid := v.SidReporter
			sid2 := strings.Split(sid, "_")
			sid2[5] = strconv.FormatInt(v.Called, 10)
			sided := strings.Join(sid2, "")
			var Logresult []Log
			//通过insertTime获取callBaseLog日志
			err = collection2.Find(bson.M{"sid": sid, "callBaseLog": bson.M{"$exists": true}}).Select(bson.M{"callBaseLog": 1}).All(&Logresult)
			if err != nil {
				panic(err)
			}
			var Logresult2 []Log
			//通过insertTime获取callBaseLog日志
			err = collection2.Find(bson.M{"sid": sided, "callBaseLog": bson.M{"$exists": true}}).Select(bson.M{"callBaseLog": 1}).All(&Logresult2)
			if err != nil {
				panic(err)
			}
			i := callquality(Logresult, Logresult2)
			if i == 1 {
				a++
			}
			if i == 2 {
				b++
			}
			if i == 3 {
				c++
			}
		}

	}
	toPromtheus2(a, b, c)
	return BaseLogresult

}
func callquality(log, log2 []Log) int {
	var lrdelay float64
	var rldelay float64
	var lrloss float64
	var rlloss float64
	var a float64
	var b float64
	var c int
	for _, v := range log {
		for _, mapv := range v.CallBaseLog {
			if mapv != "" && strings.Index(mapv, "=") >= 0 {
				strrune := []rune(mapv)
				if string(strrune[10:14]) == "ortp" {
					smap := make(map[string]string)
					strarrays := strings.Split(mapv, " ")
					for _, v := range strarrays {
						if v != "" && strings.Index(v, "=") >= 0 {
							strarray := strings.Split(v, "=")
							smap[strarray[0]] = strarray[1]
						}
					}
					//去除延迟大于20000的数据
					if d, _ := strconv.ParseFloat(smap["delay_aver"], 64); d > 20000 {
						continue
					}
					//去除延迟等于0的数据
					if d, _ := strconv.ParseFloat(smap["delay_aver"], 64); d == 0 {
						continue
					}

					if smap["sub_type"] == "CE2E_L2R" {
						delay := smap["delay_aver"]
						fdelay, _ := strconv.ParseFloat(delay, 64)

						a += fdelay
						b += 1
					}
					lrdelay = a / b
					if smap["sub_type"] == "CSR" {
						index, _ := strconv.Atoi(smap["logIndex"])
						if index > c {
							c = index
							loss := smap["a_loss_r"]
							loss = strings.Replace(loss, "%", "", -1)
							alrloss, _ := strconv.ParseFloat(loss, 64)
							lrloss = alrloss

						}
					}
				}
			}
		}
	}
	for _, v := range log2 {
		for _, mapv := range v.CallBaseLog {
			if mapv != "" && strings.Index(mapv, "=") >= 0 {
				strrune := []rune(mapv)
				if string(strrune[10:14]) == "ortp" {
					smap := make(map[string]string)
					strarrays := strings.Split(mapv, " ")
					for _, v := range strarrays {
						if v != "" && strings.Index(v, "=") >= 0 {
							strarray := strings.Split(v, "=")
							smap[strarray[0]] = strarray[1]
						}
					}
					//去除延迟大于20000的数据
					if d, _ := strconv.ParseFloat(smap["delay_aver"], 64); d > 20000 {
						continue
					}
					//去除延迟等于0的数据
					if d, _ := strconv.ParseFloat(smap["delay_aver"], 64); d == 0 {
						continue
					}
					var a float64
					var b float64
					var c int
					if smap["sub_type"] == "CE2E_L2R" {
						delay := smap["delay_aver"]
						fdelay, _ := strconv.ParseFloat(delay, 64)
						a += fdelay
						b += 1
					}
					rldelay = a / b
					if smap["sub_type"] == "CSR" {
						index, _ := strconv.Atoi(smap["logIndex"])
						if index > c {
							c = index
							loss := smap["a_loss_r"]
							loss = strings.Replace(loss, "%", "", -1)
							arlloss, _ := strconv.ParseFloat(loss, 64)
							rlloss = arlloss
						}
					}
				}
			}
		}
	}

	if lrdelay < gooddelay && rldelay < gooddelay && lrloss < goodloss && rlloss < goodloss {
		return 1
	}
	if lrdelay < zdelay && rldelay < zdelay && lrloss < zloss && rlloss < zloss {
		return 2
	}
	return 3
}
func toPromtheus(BaseLogresult []BaseLog) {
	var as, ac, al, vs, vc, vl int64
	for _, v := range BaseLogresult {
		time := v.Endtime - v.Starttime
		if time > 0 {
			var aorv string
			if v.Reporttype == 2 {
				aorv = "video"
				if time > longtime {
					vl++
				}
				if time <= shotrtime {
					vs++
				}
				if time > shotrtime && time <= longtime {
					vc++
				}
			} else {
				aorv = "audio"
				if time > longtime {
					al++
				}
				if time <= shotrtime {
					as++
				}
				if time > shotrtime && time <= longtime {
					ac++
				}
			}

			Observe(aorv, time)

		}
	}
	Observetimeedge(al, ac, as, vl, vc, vs)

}
func toPromtheus2(a, b, c int) {
	fmt.Println("优秀:", a, "中等:", b, "较差:", c)
	node2.WithLabelValues("优秀").Set(float64(a))
	node2.WithLabelValues("中等").Set(float64(b))
	node2.WithLabelValues("较差").Set(float64(c))
}
func loadConfig() {
	cfgbuf, err := ioutil.ReadFile("cfg.yaml")
	if err != nil {
		panic("not found cfg.yaml")
	}
	rfig := RConfig{}
	err = yaml.Unmarshal(cfgbuf, &rfig)
	if err != nil {
		panic("invalid cfg.yaml")
	}
	globeCfg = &rfig
	fmt.Println("Load config -'cfg.yaml'- ok...")
}
func init() {
	loadConfig() //加载配置文件
	shotrtime = globeCfg.P2P.Shorttimeedge * 1000
	longtime = globeCfg.P2P.Longtimeedge * 1000

	HistogramOptsparamMap = globeCfg.P2P.HistogramOptsparam
	SummaryOptsparamMap = globeCfg.P2P.SummaryOptsparam

	nodeh = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "rt",
		Subsystem: "H",
		Name:      "p2p",
		Help:      "calltime",
		Buckets:   prometheus.LinearBuckets(HistogramOptsparamMap["start"], HistogramOptsparamMap["width"], int(HistogramOptsparamMap["count"])),
	},
		[]string{
			"AorV",
		})

	nodes = prometheus.NewSummaryVec(prometheus.SummaryOpts{
		Namespace:  "rt",
		Subsystem:  "S",
		Name:       "p2p",
		Help:       "calltime",
		Objectives: SummaryOptsparamMap,
	},
		[]string{
			"AorV",
		})
	node = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "rt",
		Subsystem: "gauge",
		Name:      "p2p",
		Help:      "calltime",
	}, []string{
		"AorV",
		"timeedge",
	})
	node2 = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "rt",
		Subsystem: "qualiable",
		Name:      "p2p",
		Help:      "calltime",
	}, []string{
		"qualiable",
	})
	prometheus.MustRegister(nodeh)
	prometheus.MustRegister(nodes)
	prometheus.MustRegister(node)
	prometheus.MustRegister(node2)

}
func main() {

	ip := globeCfg.Gw.DBaddr       //"103.25.23.89:60013"
	db := globeCfg.Gw.DBname       //"dataAnalysis_new"
	table := globeCfg.Gw.Tablename //"report_tab"
	table2 := globeCfg.Gw.Tablename2
	gooddelay = globeCfg.P2P.Gooddelay
	zdelay = globeCfg.P2P.Zdelay
	goodloss = globeCfg.P2P.Goodloss
	zloss = globeCfg.P2P.Zloss
	//loop
	go func() {
		fmt.Println("Program startup ok...")
		//获取callBaseLog数据
		for {
			toPromtheus(mongodbToBaselog(ip, db, table, table2))

			//是否推送数据给PushGatway
			if globeCfg.Output.PushGateway {
				var info = make(map[string]string)
				info["monitorID"] = globeCfg.Output.MonitorID
				if err := push.FromGatherer("rt", info, globeCfg.Output.PushGatewayAddr, prometheus.DefaultGatherer); err != nil {
					fmt.Println("FromGatherer:", err)
				}
			}
			fmt.Println(time.Now())
			time.Sleep(time.Duration(globeCfg.Output.Period) * time.Second)

		}
	}()
	//设置prometheus监听的ip和端口
	if globeCfg.Output.Prometheus {
		go func() {
			fmt.Println("ip", globeCfg.Gw.Addr)
			fmt.Println("port", globeCfg.Gw.HttpListenPort)
			http.Handle("/metrics", promhttp.Handler())
			http.ListenAndServe(fmt.Sprintf("%s:%d", globeCfg.Gw.Addr, globeCfg.Gw.HttpListenPort), nil)

		}()
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c)
	//	signal.Notify(c, os.Interrupt, os.Kill)
	s := <-c
	fmt.Println("asd")
	fmt.Println("exitss", s)

}
