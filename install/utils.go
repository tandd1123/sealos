package install

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/wonderivan/logger"
	"golang.org/x/crypto/ssh"
)

//username
var (
	User           string
	Passwd         string
	PrivateKeyFile string
	KubeadmFile    string
	Version        string
)

const oneMBByte = 1024 * 1024

func AddrReformat(host string) string {
	if strings.Index(host, ":") == -1 {
		host = fmt.Sprintf("%s:22", host)
	}
	return host
}

func ReturnCmd(host, cmd string) string {
	session, _ := Connect(User, Passwd, PrivateKeyFile, host)
	defer session.Close()
	b, _ := session.CombinedOutput(cmd)
	return string(b)
}

func GetFileSize(url string) int {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{Transport: tr}
	resp, err := client.Get(url)
	if err != nil {
		panic(err)
	}
	resp.Body.Close()
	return int(resp.ContentLength)
}

func WatchFileSize(host, filename string, size int) {
	t := time.NewTicker(3 * time.Second) //every 3s check file
	defer t.Stop()
	for {
		select {
		case <-t.C:
			length := ReturnCmd(host, "ls -l "+filename+" | awk '{print $5}'")
			length = strings.Replace(length, "\n", "", -1)
			length = strings.Replace(length, "\r", "", -1)
			lengthByte, _ := strconv.Atoi(length)
			if lengthByte == size {
				t.Stop()
			}
			lengthFloat := float64(lengthByte)
			value, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", lengthFloat/oneMBByte), 64)
			logger.Alert("transfer total size is:", value, "MB")
		}
	}
}

//Cmd is
func Cmd(host string, cmd string) []byte {
	logger.Info(host, "    ", cmd)
	session, err := Connect(User, Passwd, PrivateKeyFile, host)
	if err != nil {
		logger.Error("	Error create ssh session failed", err)
		panic(1)
	}
	defer session.Close()

	b, err := session.CombinedOutput(cmd)
	logger.Debug("command result is:", string(b))
	if err != nil {
		logger.Error("	Error exec command failed", err)
		panic(1)
	}
	return b
}

func RemoteFilExist(host, remoteFilePath string) bool {
	// if remote file is
	// ls -l | grep aa | wc -l
	remoteFileName := path.Base(remoteFilePath) // aa
	remoteFileDirName := path.Dir(remoteFilePath)
	remoteFileCommand := fmt.Sprintf("ls -l %s | grep %s | wc -l", remoteFileDirName, remoteFileName)
	data := bytes.Replace(Cmd(host, remoteFileCommand), []byte("\r"), []byte(""), -1)
	data = bytes.Replace(data, []byte("\n"), []byte(""), -1)

	count, err := strconv.Atoi(string(data))
	if err != nil {
		logger.Error("RemoteFilExist:", err)
		panic(1)
	}
	if count == 0 {
		return false
	} else {
		return true
	}
}

//Copy is
func Copy(host, localFilePath, remoteFilePath string) {
	sftpClient, err := SftpConnect(User, Passwd, PrivateKeyFile, host)
	if err != nil {
		logger.Error("scpCopy:", err)
		panic(1)
	}
	defer sftpClient.Close()
	srcFile, err := os.Open(localFilePath)
	if err != nil {
		logger.Error("scpCopy:", err)
		panic(1)
	}
	defer srcFile.Close()

	dstFile, err := sftpClient.Create(remoteFilePath)
	if err != nil {
		logger.Error("scpCopy:", err)
		panic(1)
	}
	defer dstFile.Close()
	buf := make([]byte, 100*oneMBByte) //100mb
	totalMB := 0
	for {
		n, _ := srcFile.Read(buf)
		if n == 0 {
			break
		}
		length, _ := dstFile.Write(buf[0:n])
		totalMB += length / oneMBByte
		logger.Alert("transfer total size is:", totalMB, "MB")
	}
}
func readFile(name string) string {
	content, err := ioutil.ReadFile(name)
	if err != nil {
		logger.Error(err)
		return ""
	}

	return string(content)
}
func sshAuthMethod(passwd, pkFile string) ssh.AuthMethod {
	var am ssh.AuthMethod
	if passwd != "" {
		am = ssh.Password(passwd)
	} else {
		pkData := readFile(pkFile)
		pk, _ := ssh.ParsePrivateKey([]byte(pkData))
		am = ssh.PublicKeys(pk)
	}
	return am
}

//Connect is
func Connect(user, passwd, pkFile, host string) (*ssh.Session, error) {
	auth := []ssh.AuthMethod{sshAuthMethod(passwd, pkFile)}
	config := ssh.Config{
		Ciphers: []string{"aes128-ctr", "aes192-ctr", "aes256-ctr", "aes128-gcm@openssh.com", "arcfour256", "arcfour128", "aes128-cbc", "3des-cbc", "aes192-cbc", "aes256-cbc"},
	}

	clientConfig := &ssh.ClientConfig{
		User:    user,
		Auth:    auth,
		Timeout: time.Duration(5) * time.Minute,
		Config:  config,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		},
	}

	addr := AddrReformat(host)
	client, err := ssh.Dial("tcp", addr, clientConfig)
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          0,     // disable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	if err := session.RequestPty("xterm", 80, 40, modes); err != nil {
		return nil, err
	}

	return session, nil
}

//SftpConnect  is
func SftpConnect(user, passwd, pkFile, host string) (*sftp.Client, error) {
	var (
		auth         []ssh.AuthMethod
		addr         string
		clientConfig *ssh.ClientConfig
		sshClient    *ssh.Client
		sftpClient   *sftp.Client
		err          error
	)
	// get auth method
	auth = make([]ssh.AuthMethod, 0)
	auth = append(auth, sshAuthMethod(passwd, pkFile))

	clientConfig = &ssh.ClientConfig{
		User:    user,
		Auth:    auth,
		Timeout: 30 * time.Second,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		},
	}

	// connet to ssh
	addr = AddrReformat(host)

	if sshClient, err = ssh.Dial("tcp", addr, clientConfig); err != nil {
		return nil, err
	}

	// create sftp client
	if sftpClient, err = sftp.NewClient(sshClient); err != nil {
		return nil, err
	}

	return sftpClient, nil
}
func SendPackage(url string, hosts []string) {
	pkg := path.Base(url)
	//only http
	isHttp := strings.HasPrefix(url, "http")
	wgetCommand := ""
	if isHttp {
		wgetParam := ""
		if strings.HasPrefix(url, "https") {
			wgetParam = "--no-check-certificate"
		}
		wgetCommand = fmt.Sprintf(" wget %s ", wgetParam)
	}
	remoteCmd := fmt.Sprintf("cd /root &&  %s %s && tar zxvf %s", wgetCommand, url, pkg)
	localCmd := fmt.Sprintf("cd /root && rm -rf kube && tar zxvf %s ", pkg)
	kubeCmd := "cd /root/kube/shell && sh init.sh"
	kubeLocal := fmt.Sprintf("/root/%s", pkg)
	var wm sync.WaitGroup
	for _, host := range hosts {
		wm.Add(1)
		go func(host string) {
			defer wm.Done()
			logger.Debug("please wait for tar zxvf exec")
			if RemoteFilExist(host, kubeLocal) {
				logger.Warn("host is ", host, ", SendPackage: file is exist")
				Cmd(host, localCmd)
			} else {
				if isHttp {
					go WatchFileSize(host, kubeLocal, GetFileSize(url))
					Cmd(host, remoteCmd)
				} else {
					Copy(host, url, kubeLocal)
					Cmd(host, localCmd)
				}
			}
			Cmd(host, kubeCmd)
		}(host)
	}
	wm.Wait()
}
