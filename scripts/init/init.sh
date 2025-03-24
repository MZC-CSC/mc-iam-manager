#!/bin/bash
source ./.env

login(){
    read -p "Enter the platforadmin ID: " MCIAMMANAGER_PLATFORMADMIN_ID
    #read -s -p "Enter the platforadmin password: " MCIAMMANAGER_PLATFORMADMIN_PASSWORD
    read -p "Enter the platforadmin password: " MCIAMMANAGER_PLATFORMADMIN_PASSWORD
    
    response=$(curl --location --silent --header 'Content-Type: application/json' --data '{
        "id":"'"$MCIAMMANAGER_PLATFORMADMIN_ID"'",
        "password":"'"$MCIAMMANAGER_PLATFORMADMIN_PASSWORD"'"
    }' "$MCIAMMANAGER_HOST/api/auth/login")
    echo + $MCIAMMANAGER_HOST $response
    MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN="$(echo "$response" | jq -r '.access_token')"
    echo + $MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN
}

initResourceDatafromApiYaml(){
    echo + $MCADMINCLI_APIYAML
    echo + $MCIAMMANAGER_HOST
    echo + $MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN
    wget -q -O ./api.yaml $MCADMINCLI_APIYAML
    curl --location "$MCIAMMANAGER_HOST/api/resource/file/framework/all" \
    --header "Authorization: Bearer $MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN" \
    --form "file=@./api.yaml"
}

initMenuDatafromMenuYaml(){
    echo + $MCIAMMANAGER_HOST
    echo + $MCWEBCONSOLE_MENUYAML
    wget -q -O ./mcwebconsoleMenu.yaml $MCWEBCONSOLE_MENUYAML
    curl --location "$MCIAMMANAGER_HOST/api/resource/file/framework/mc-web-console/menu" \
    --header "Authorization: Bearer $MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN" \
    --form "file=@./mcwebconsoleMenu.yaml"
}

initRoleData(){
    echo + $MCIAMMANAGER_HOST
    echo + $PREDEFINED_ROLE
    IFS=',' read -r -a roles <<< "$PREDEFINED_ROLE"
    for role in "${roles[@]}"
    do
        echo "Creating role: $role"
        json_data=$(jq -n --arg name "$role" --arg description "$role Role" \
        '{name: $name, description: $description}')

        echo "json_data: $json_data"

        curl -s -X POST \
        --header 'Content-Type: application/json' \
        --header "Authorization: Bearer $MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN" \
        --data "$json_data" \
        "$MCIAMMANAGER_HOST/api/role"
    done
}

getPermissionCSV(){
    wget --header="Authorization: Bearer $MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN" -O ./permission.csv $MCIAMMANAGER_HOST/api/permission/file/framework/all
}

postPermissionCSV(){
    curl --location "$MCIAMMANAGER_HOST/api/permission/file/framework/all" \
    --header "Authorization: Bearer $MCIAMMANAGER_PLATFORMADMIN_ACCESSTOKEN" \
    --form "file=@./permission.csv"
}

show_menu() {
    # clear
    echo
    echo "0. exit"
    echo
    echo "1. login"
    echo
    echo "2. Init Resource Data from api.yaml"
    echo "  (MCADMINCLI_APIYAML: $MCADMINCLI_APIYAML)"
    echo
    echo "3. Init Menu Data from menu.yaml"
    echo "  (MCWEBCONSOLE_MENUYAML: $MCWEBCONSOLE_MENUYAML)"
    echo
    echo "4. Init Role Data PREDEFINED_ROLE"
    echo "  (PREDEFINED_ROLE: $PREDEFINED_ROLE)"
    echo
    echo "5. Get permission CSV"
    echo
    echo "6. Update permission CSV "
    echo "  (./permission.csv)"
    echo
    echo "99. auto init"
    echo
    echo "--------------------"
    echo -n "select Number : "
}

donePrint() {
    echo
    echo "done!"
    echo
    echo "Press anykey to continue..."
    echo
}

autoInit() {
    login
    initResourceDatafromApiYaml
    initMenuDatafromMenuYaml
    initRoleData
    getPermissionCSV
    
    echo
    echo "edit './permission.csv' and press any key to proceed.."
    echo
    read -n 1 -s

    postPermissionCSV
}


read_option() {
    local choice
    read choice
    case $choice in
        0) exit 0 ;;
        1) login;;
        2) initResourceDatafromApiYaml;;
        3) initMenuDatafromMenuYaml;;
        4) initRoleData;;
        5) getPermissionCSV;;
        6) postPermissionCSV;;
        99) autoInit;;
        *) echo "wrong selection"
    esac
}


while true
do
    show_menu
    read_option
    donePrint
    read -n 1 -s
done