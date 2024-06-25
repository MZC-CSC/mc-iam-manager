package actions

import (
	"fmt"
	"log"
	"mc_iam_manager/handler/securitykeyprovider"
	"mc_iam_manager/handler/securitykeyprovider/alibaba"
	"mc_iam_manager/handler/securitykeyprovider/aws"
	"net/http"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"

	sdkalibaba "github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	alicred "github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"github.com/gobuffalo/buffalo"
)

func AuthSecuritykeyProviderHandler(c buffalo.Context) error {
	providers := c.Param("providers")
	var providerarr []string
	if providers != "" {
		providerarr = strings.Split(providers, ",")
	} else {
		providerarr = []string{"aws", "alibaba"}
	}
	var mciamCspCredentialsResponse securitykeyprovider.MciamCspCredentialsResponse
	for _, provider := range providerarr {
		switch provider {
		case "aws":
			t := aws.AWS{}
			res, err := securitykeyprovider.GetKey(c, t)
			if err != nil {
				log.Println(err.Error())
				return c.Render(http.StatusInternalServerError, r.JSON(map[string]string{"error : aws :": err.Error()}))
			}
			mciamCspCredentialsResponse.CspCredentials = append(mciamCspCredentialsResponse.CspCredentials, *res)
		case "alibaba":
			t := alibaba.Alibaba{}
			res, err := securitykeyprovider.GetKey(c, t)
			if err != nil {
				log.Println(err.Error())
				return c.Render(http.StatusInternalServerError, r.JSON(map[string]string{"error : alibaba :": err.Error()}))
			}
			mciamCspCredentialsResponse.CspCredentials = append(mciamCspCredentialsResponse.CspCredentials, *res)
		default:

		}
	}
	return c.Render(http.StatusOK, r.JSON(mciamCspCredentialsResponse))
}

type AuthGetVmAWSHandlerRequest struct {
	AccessKey    string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	SessionToken string `json:"sessionToken"`
}

func AuthGetVmAWSHandler(c buffalo.Context) error {
	var sts AuthGetVmAWSHandlerRequest
	err := c.Bind(&sts)
	if err != nil {
		log.Println(err.Error())
		return c.Render(http.StatusInternalServerError, r.JSON(map[string]string{"error : AWS :": err.Error()}))
	}

	sess, err := session.NewSession(&sdkaws.Config{
		Region:      sdkaws.String("ap-northeast-2"),
		Credentials: credentials.NewStaticCredentials(sts.AccessKey, sts.SecretKey, sts.SessionToken),
	})
	if err != nil {
		log.Println(err.Error())
		return c.Render(http.StatusInternalServerError, r.JSON(map[string]string{"error : AWS :": err.Error()}))
	}
	ec2Svc := ec2.New(sess)
	result, err := ec2Svc.DescribeInstances(nil)
	if err != nil {
		log.Println(err.Error())
		return c.Render(http.StatusInternalServerError, r.JSON(map[string]string{"error : AWS :": err.Error()}))
	}
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			fmt.Printf("Instance ID: %s\n", *instance.InstanceId)
		}
	}
	return c.Render(http.StatusOK, r.JSON(result))
}

type AuthGetVmAlibabaHandlerRequest struct {
	AccessKey    string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	SessionToken string `json:"sessionToken"`
}

func AuthGetVmAlibabaHandler(c buffalo.Context) error {
	var sts AuthGetVmAlibabaHandlerRequest
	err := c.Bind(&sts)
	if err != nil {
		log.Println(err.Error())
		return c.Render(http.StatusInternalServerError, r.JSON(map[string]string{"error : alibaba :": err.Error()}))
	}

	config := sdkalibaba.NewConfig()
	credential := alicred.NewStsTokenCredential(sts.AccessKey, sts.SecretKey, sts.SessionToken)

	client, err := ecs.NewClientWithOptions("cn-hongkong", config, credential)
	if err != nil {
		log.Fatalf("Failed to create ECS client: %s", err)
	}

	request := ecs.CreateDescribeInstancesRequest()
	request.Scheme = "https"

	response, err := client.DescribeInstances(request)
	if err != nil {
		log.Fatalf("Failed to describe instances: %s", err)
	}

	for _, instance := range response.Instances.Instance {
		fmt.Printf("Instance ID: %s\n", instance.InstanceId)
	}
	return c.Render(http.StatusOK, r.JSON(response.Instances))
}
