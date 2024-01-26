console.log("starting worker");
const encoder = new TextEncoder();
let errCnt = 0;
let closeCnt = 0;

function connectWebSocket() {
    const socket = new WebSocket('ws://127.0.0.1:9001/ws');

    onmessage = function(event) {
        socket.send(encoder.encode(JSON.stringify(event.data)));
    };

    socket.onopen = function () {
        console.log('WebSocket连接已建立');
    };

    socket.onmessage = function (event) {
        let obj = JSON.parse(event.data);
        postMessage(obj);
    };

    socket.onclose = function (event) {
        console.log('WebSocket连接已关闭');
        // 连接关闭后等待 5 秒重新建立连接
        closeCnt++;
        if(closeCnt > 10){
            console.log("连接关闭已超过 10 次, 不再重试")
            return
        }
        setTimeout(connectWebSocket, 10000);
    };

    socket.onerror = function (error) {
        console.error('WebSocket连接发生错误:', error);
        // 连接错误后等待 5 秒重新建立连接
        errCnt++;
        if (errCnt > 10){
            console.log("连接错误已超过 10 次, 不再重试")
            return
        }
        setTimeout(connectWebSocket, 10000);
    };
}

// 启动 WebSocket 连接
connectWebSocket();
