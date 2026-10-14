package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"gopkg.in/yaml.v3"
)

func TestShippedConfigsPassStrictValidation(t *testing.T){
	for _,name:=range []string{"CODEUP_ORG_ID","CODEUP_TOKEN","KIMI_API_KEY","GITHUB_TOKEN"}{t.Setenv(name,"fixture")}
	for _,name:=range []string{"AGENT_ADMIN_TOKEN","AGENT_SERVER_ADDR","AGENT_REVIEW_DRY_RUN"}{t.Setenv(name,"")}
	paths,err:=filepath.Glob("../../configs/config*.yaml");if err!=nil{t.Fatal(err)}
	if len(paths)<5{t.Fatal("configuration fixtures missing")}
	for _,p:=range paths{t.Run(p,func(t *testing.T){c,err:=Load(p);if err!=nil{t.Fatal(err)};if err:=c.Validate();err!=nil{t.Fatal(err)}})}
}

func TestComposePersistsStateAndRequiresAdminSecret(t *testing.T){
	b,err:=os.ReadFile("../../docker-compose.yml");if err!=nil{t.Fatal(err)}
	var cfg struct{Services map[string]struct{Environment []string `yaml:"environment"`;Volumes []string `yaml:"volumes"`;Ports []string `yaml:"ports"`} `yaml:"services"`}
	if err:=yaml.Unmarshal(b,&cfg);err!=nil{t.Fatal(err)}
	s:=cfg.Services["pr-agent"]
	for _,mount:=range []string{"./data:/app/data","./docs:/app/docs"}{found:=false;for _,v:=range s.Volumes{if v==mount{found=true}};if !found{t.Errorf("missing writable persistent mount %s",mount)}}
	env:=strings.Join(s.Environment,"\n")
	for _,required:=range []string{"CODEUP_TOKEN=","CODEUP_ORG_ID=","AGENT_ADMIN_TOKEN=${AGENT_ADMIN_TOKEN:?","AGENT_SERVER_ADDR=0.0.0.0:8081"}{if !strings.Contains(env,required){t.Errorf("missing %s",required)}}
	if len(s.Ports)!=1||!strings.HasPrefix(s.Ports[0],"127.0.0.1:"){t.Fatal("management port exposed on all host interfaces")}
}
