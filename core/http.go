package core

import (
	"context"
	"drdos/config"
	"drdos/core/api"
	"drdos/utils"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
)

var (
	totalcount int
	interval   int = 10000
	flag       int
	ctx        context.Context
	cancel     context.CancelFunc
)

func HttpMain() {
	router := gin.Default()
	router.LoadHTMLGlob("core/html/*.html")
	router.StaticFS("vendor", http.Dir("core/statics/vendor"))
	router.StaticFS("scss", http.Dir("core/statics/scss"))
	router.StaticFS("js", http.Dir("core/statics/js"))
	router.StaticFS("css", http.Dir("core/statics/css"))
	router.StaticFS("img", http.Dir("core/statics/img"))

	router.GET("/dashboard", dashboard)
	router.GET("/file", file)

	/*
		API接口
	*/
	password := utils.RandomString(8)
	utils.ColorPrint("Password : "+password, "info")
	authorized := router.Group("/api", gin.BasicAuth(gin.Accounts{
		"admin": password,
	}))

	authorized.GET("/info", info)
	authorized.POST("/apicheck", apicheck)
	authorized.GET("/status", status)
	authorized.POST("/check", ipcheck)
	authorized.GET("/cancel", cancelcheck)
	authorized.GET("/loadfiles", loadsList)
	authorized.GET("/download/:filename", download)
	authorized.POST("/upload", upload)
	authorized.POST("/checkdb", checkdb)
	authorized.GET("/dbquery", dbquery)

	router.Run(":" + strconv.Itoa(config.HttpPort))
}

func dbquery(c *gin.Context) {
	protocol := c.Query("type")
	if port, ok := utils.Typemap[protocol]; ok {
		ctx, cancel = context.WithCancel(context.Background())
		iplists, err := utils.Query(port, 1, ctx)
		if err != nil {
			c.JSON(200, gin.H{"code": 400, "msg": "Query failed"})
			return
		}
		if len(iplists) == 0 {
			c.JSON(200, gin.H{"code": 400, "msg": "None ip found"})
			return
		}
		c.Header("Content-Disposition", `attachment; filename=`+protocol+".txt")
		c.Header("Content-Transfer-Encoding", "binary")
		Data := []byte(strings.Join(iplists, "\n"))
		c.Data(200, "application/octet-stream", Data)

	} else {
		c.JSON(200, gin.H{"code": 400, "msg": "Protocol not found"})
	}
}

/*
	这里存在的问题，如果Query的时间过长，
*/
func checkdb(c *gin.Context) {
	var input checkBody
	if GlobalLock == 1 {
		c.JSON(200, gin.H{"code": 400, "msg": "A task is running"})
		return
	}
	err := c.BindJSON(&input)
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": "Json parse error"})
		return
	}

	ctx, cancel = context.WithCancel(context.Background())
	GlobalLock = 1
	go func(ctx context.Context) {
		var iplists []string
		tmp, err := utils.Query(utils.Typemap[input.Type], 1, ctx)
		if err != nil {
			return
		}
		iplists = append(iplists, tmp...)
		tmp, err = utils.Query(utils.Typemap[input.Type], 0, ctx)
		if err != nil {
			return
		}
		iplists = append(iplists, tmp...)
		totalcount = len(iplists)
		GlobalLock = 0
		_, err = Check(iplists, input.Type, uint(interval), input.Sip, ctx)
		if err != nil {
			fmt.Println(err)
			return
		}
	}(ctx)
	c.JSON(200, gin.H{"code": 200, "msg": "Running"})
}

func dashboard(c *gin.Context) {
	c.HTML(http.StatusOK, "blank.html", gin.H{})
}

func file(c *gin.Context) {
	c.HTML(http.StatusOK, "file.html", gin.H{})
}

func info(c *gin.Context) {
	var cpuresult int
	var memresult int

	cpupercent, err := cpu.Percent(time.Second, false)
	if err != nil {
		fmt.Println(err)
		cpuresult = 0
	} else {
		cpuresult = int(cpupercent[0])
	}

	mempercent, err := mem.VirtualMemory()
	if err != nil {
		fmt.Println(err)
		memresult = 0
	} else {
		memresult = int(mempercent.UsedPercent)
	}

	c.String(http.StatusOK, strconv.Itoa(cpuresult)+" "+strconv.Itoa(memresult))
}

func status(c *gin.Context) {
	var process int
	if totalcount == 0 {
		process = 0
	} else {
		process = int((SendIndex * 100) / totalcount)
	}
	c.String(http.StatusOK, strconv.Itoa(totalcount)+" "+strconv.Itoa(RecvIndex)+" "+strconv.Itoa(process))
}

func loadsList(c *gin.Context) {
	var result []string
	fileInfoList, err := ioutil.ReadDir(utils.Dir + "/data/loads/")
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": "File dir get failed"})
		return
	}
	for i := range fileInfoList {
		result = append(result, fileInfoList[i].Name())
	}
	c.JSON(200, gin.H{"code": 200, "msg": result})
}

func apicheck(c *gin.Context) {
	var (
		input  checkBody
		iplist []string
		err    error
	)

	if GlobalLock == 1 {
		c.JSON(200, gin.H{"code": 400, "msg": "A task is running"})
		return
	}

	ctx, cancel = context.WithCancel(context.Background())

	err = c.BindJSON(&input)
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": "Json parse error"})
		return
	}

	err = basecheck(input)
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": fmt.Sprintf("%s", err)})
		return
	}

	switch {
	case config.ShodanApi != "":
		fmt.Println("[*] Shodan API Searching")
		for page := 1; page <= config.ShodanPage; page++ {
			tmplist, err := api.Shodan(utils.Typemap[input.Type], uint(page))
			if err != nil {
				break
			}
			iplist = append(iplist, tmplist...)
		}
		fmt.Println("[+] Shodan API Searching Finished")
		fallthrough
	case config.ZoomeyeApi != "":
		fmt.Println("[*] Zoomeye API Searching")
		for page := 1; page <= config.ZoomeyePage; page++ {
			tmplist, err := api.Zoomeye(utils.Typemap[input.Type], page)
			if err != nil {
				break
			}
			iplist = append(iplist, tmplist...)
		}
		fmt.Println("[+] Zoomeye API Searching Finished")
	}
	iplist = utils.RemoveRepeatedElement(iplist)

	// 如果为0代表
	if len(iplist) == 0 {
		c.JSON(200, gin.H{"code": 400, "msg": "None IP found"})
	} else {
		go func(ctx context.Context) {
			totalcount = len(iplist)
			_, err := Check(iplist, input.Type, uint(interval), input.Sip, ctx)
			if err != nil {
				fmt.Println(err)
			}
		}(ctx)
	}
	c.JSON(200, gin.H{"code": 200, "msg": "Running"})
}

type checkBody struct {
	Sip      string `json:sip`
	Dip      string `json:dip`
	Type     string `json:type`
	Loadfile string `json:loadfile`
}

func download(c *gin.Context) {
	dir := c.Query("dir")
	name := c.Param("filename")
	switch {
	case dir == "loadfile":
		c.File(utils.Dir + "/data/loads/" + name)
	default:
		c.JSON(200, gin.H{"code": 400, "msg": "dir error"})
	}
}

func upload(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": "upload error"})
		return
	}

	path, err := utils.FileNameCheck(file.Filename)
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": "filename error"})
		return
	}
	fmt.Println(path)
	ok := c.SaveUploadedFile(file, utils.Dir+"/data/loads/"+path)
	if ok != nil {
		c.JSON(200, gin.H{"code": 400, "msg": "file save error"})
	}
	c.JSON(200, gin.H{"code": 200, "msg": "upload success"})
}

func ipcheck(c *gin.Context) {
	if GlobalLock == 1 {
		c.JSON(200, gin.H{"code": 400, "msg": "A task is running"})
		return
	}

	ctx, cancel = context.WithCancel(context.Background())

	var input checkBody
	var iplist []string
	var err error

	err = c.BindJSON(&input)
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": " Json parse error"})
		return
	}

	err = basecheck(input)
	if err != nil {
		c.JSON(200, gin.H{"code": 400, "msg": fmt.Sprintf("%s", err)})
		return
	}

	switch {
	case input.Loadfile != "":
		path, err := utils.FileNameCheck(input.Loadfile)
		if err != nil {
			c.JSON(200, gin.H{"code": 400, "msg": "filename error"})
			return
		}
		iplist, err = utils.FileLoads(utils.Dir + "/data/loads/" + path)
		if err != nil {
			c.JSON(200, gin.H{"code": 400, "msg": "File not found"})
			return
		}
	// 3. 判断是否从IP地址范围输入
	case input.Dip != "":
		iprange := input.Dip
		if strings.Contains(iprange, "/") {
			s := strings.Split(iprange, "/")
			tmp, err := strconv.Atoi(s[1])
			if err != nil {
				c.JSON(200, gin.H{"code": 400, "msg": "wrong CIDR range"})
				return
			}
			if tmp < 14 {
				c.JSON(200, gin.H{"code": 400, "msg": "Range too large"})
				return
			}

			iplist, err = utils.Hosts(iprange)
			if err != nil {
				c.JSON(200, gin.H{"code": 400, "msg": "wrong CIDR range"})
				return
			}
		} else {
			check := net.ParseIP(iprange)
			if check == nil {
				c.JSON(200, gin.H{"code": 400, "msg": "wrong IP address"})
				return
			}
			fmt.Println("2")
			iplist = []string{iprange}
		}
	}

	if len(iplist) == 0 {
		c.JSON(200, gin.H{"code": 400, "msg": "None IP found"})
		return
	}

	go func(ctx context.Context) {
		totalcount = len(iplist)
		_, err := Check(iplist, input.Type, uint(interval), input.Sip, ctx)
		if err != nil {
			fmt.Println(err)
		}
	}(ctx)

	c.JSON(200, gin.H{"code": 200, "msg": "Running"})
}

func cancelcheck(c *gin.Context) {
	if GlobalLock == 0 {
		c.JSON(200, gin.H{"code": 400, "msg": "No task!"})
		return
	}
	cancel()
	c.JSON(200, gin.H{"code": 200, "msg": "Cancel called"})
}

func basecheck(input checkBody) error {
	var err error
	check := net.ParseIP(input.Sip)
	if check == nil {
		err = errors.New("ip check failed")
		return err
	}
	if _, ok := utils.Typemap[input.Type]; !ok {
		err = errors.New("type not found")
		return err
	}
	return nil
}
