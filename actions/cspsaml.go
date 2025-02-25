package actions

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gobuffalo/buffalo"

	//common
	samllogin "gocloak/util/samlHandler"

	//aws
	awscfg "gocloak/util/samlHandler/aws/pkg/cfg"
	awscreds "gocloak/util/samlHandler/aws/pkg/creds"

	//ali
	alicfg "gocloak/util/samlHandler/alibaba/pkg/cfg"
	alicreds "gocloak/util/samlHandler/alibaba/pkg/creds"
)

func AwsSamlSTSKey(c buffalo.Context) error {

	account := &awscfg.IDPAccount{
		URL:                  os.Getenv("SAML_IDP_Initiated_URL_AWS"),
		Username:             os.Getenv("SAML_user"),
		Provider:             "KeyCloak",
		MFA:                  "Auto",
		SkipVerify:           false,
		AmazonWebservicesURN: "urn:amazon:webservices",
		SessionDuration:      900,
		Profile:              "saml",
		RoleARN:              "",
		Region:               "",
	}

	loginDetails := &awscreds.LoginDetails{
		URL:          account.URL,
		Username:     account.Username,
		Password:     os.Getenv("SAML_password"),
		MFAToken:     "",
		DuoMFAOption: "",
	}

	cred, err := samllogin.LoginAWS(account, loginDetails)
	if err != nil {
		return c.Render(http.StatusOK, r.JSON(err.Error()))
	}

	fmt.Println("########## AWSAccessKey  : ", cred.AWSAccessKey)
	fmt.Println("########## AWSSecretKey  : ", cred.AWSSecretKey)
	fmt.Println("########## AWSSecurityToken  : ", cred.AWSSecurityToken)
	fmt.Println("########## AWSSessionToken  : ", cred.AWSSessionToken)
	fmt.Println("########## Expires  : ", cred.Expires.Format(time.RFC3339))

	return c.Render(http.StatusOK, r.JSON("success"))
}

func AliSamlSTSKey(c buffalo.Context) error {

	account := &alicfg.IDPAccount{
		URL:             os.Getenv("SAML_IDP_Initiated_URL_ALI"),
		Username:        os.Getenv("SAML_user"),
		Provider:        "KeyCloak",
		MFA:             "Auto",
		SkipVerify:      false,
		AlibabaCloudURN: "urn:alibaba:cloudcomputing",
		SessionDuration: 900,
		Profile:         "saml",
		RoleARN:         "",
		Region:          "",
	}

	loginDetails := &alicreds.LoginDetails{
		URL:          account.URL,
		Username:     account.Username,
		Password:     os.Getenv("SAML_password"),
		MFAToken:     "",
		DuoMFAOption: "",
	}

	cred, err := samllogin.LoginALI(account, loginDetails)
	if err != nil {
		return c.Render(http.StatusOK, r.JSON(err.Error()))
	}

	fmt.Println("########## AliCloudAccessKey  : ", cred.AliCloudAccessKey)
	fmt.Println("########## AliCloudSecretKey  : ", cred.AliCloudSecretKey)
	fmt.Println("########## AliCloudSecurityToken  : ", cred.AliCloudSecurityToken)
	fmt.Println("########## AliCloudSessionToken  : ", cred.AliCloudSessionToken)
	// fmt.Println("########## Expires  : ", cred.Expires.Format(time.RFC3339)) // TODO : 왜없지..?

	return c.Render(http.StatusOK, r.JSON("success"))
}
