const canvas = document.getElementById('canvas');
const ctx = canvas.getContext('2d');
const colorPicker = document.getElementById('color');
const widthSlider = document.getElementById('width');
const widthDisplay = document.getElementById('widthDisplay');
const clearBtn = document.getElementById('clearBtn');
const statusDiv = document.getElementById('status');
const modeButtons = document.querySelectorAll('.mode-btn');

let isDrawing = false;
let lastX = 0;
let lastY = 0;
let startX = 0;
let startY = 0;
let ws = null;
let currentMode = 'pen';
let snapshot = null;

// Responsive canvas sizing
function resizeCanvas() {
    const wrapper = document.querySelector('.canvas-wrapper');
    const maxWidth = wrapper.clientWidth;
    const maxHeight = wrapper.clientHeight;

    // Use full available space - try to maximize canvas size
    let width = maxWidth;
    let height = maxHeight;

    // Calculate which dimension is the limiting factor
    const widthRatio = maxWidth / maxHeight;
    const targetRatio = 16 / 9;

    if (widthRatio > targetRatio) {
        // Width is larger, constrain by height
        width = Math.floor(maxHeight * targetRatio);
        height = maxHeight;
    } else {
        // Height is larger, constrain by width
        width = maxWidth;
        height = Math.floor(maxWidth / targetRatio);
    }

    // Only resize if dimensions changed significantly
    if (Math.abs(canvas.width - width) < 10 && Math.abs(canvas.height - height) < 10) return;

    // Store current canvas data
    const tempCanvas = document.createElement('canvas');
    const tempCtx = tempCanvas.getContext('2d');
    tempCanvas.width = canvas.width;
    tempCanvas.height = canvas.height;
    tempCtx.drawImage(canvas, 0, 0);

    // Resize canvas
    canvas.width = width;
    canvas.height = height;

    // Restore canvas data (scaled)
    ctx.drawImage(tempCanvas, 0, 0, tempCanvas.width, tempCanvas.height, 0, 0, width, height);
}

// Initialize canvas size after DOM is ready
setTimeout(() => {
    resizeCanvas();
}, 100);

// Handle window resize
let resizeTimeout;
window.addEventListener('resize', () => {
    clearTimeout(resizeTimeout);
    resizeTimeout = setTimeout(() => {
        resizeCanvas();
    }, 250);
});

// Mode selection
modeButtons.forEach(btn => {
    btn.addEventListener('click', () => {
        modeButtons.forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        currentMode = btn.dataset.mode;
    });
});

// Color preset selection
const colorPresets = document.querySelectorAll('.color-preset');
colorPresets.forEach(btn => {
    btn.addEventListener('click', () => {
        const color = btn.dataset.color;
        colorPicker.value = color;
    });
});

// Update width display
widthSlider.addEventListener('input', (e) => {
    widthDisplay.textContent = e.target.value;
});

// WebSocket connection
function connect() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const basePath = location.pathname.endsWith('/') ? location.pathname : (location.pathname + '/');
    ws = new WebSocket(protocol + '//' + window.location.host + basePath + 'ws');

    ws.onopen = () => {
        statusDiv.textContent = '✓ Connected - Draw to collaborate!';
        statusDiv.className = 'status connected';
    };

    ws.onclose = () => {
        statusDiv.textContent = '✗ Disconnected - Reconnecting...';
        statusDiv.className = 'status disconnected';
        setTimeout(connect, 2000);
    };

    ws.onerror = (err) => {
        console.error('WebSocket error:', err);
    };

    ws.onmessage = (event) => {
        const msg = JSON.parse(event.data);

        if (msg.type === 'draw') {
            drawLine(msg.prevX, msg.prevY, msg.x, msg.y, msg.color, msg.width);
        } else if (msg.type === 'shape') {
            drawShape(msg.mode, msg.startX, msg.startY, msg.endX, msg.endY, msg.color, msg.width);
        } else if (msg.type === 'text') {
            drawText(msg.text, msg.x, msg.y, msg.color, msg.width);
        } else if (msg.type === 'clear') {
            ctx.clearRect(0, 0, canvas.width, canvas.height);
        }
    };
}

// Drawing functions
function drawLine(x1, y1, x2, y2, color, width) {
    ctx.beginPath();
    ctx.strokeStyle = color;
    ctx.lineWidth = width;
    ctx.lineCap = 'round';
    ctx.lineJoin = 'round';
    ctx.moveTo(x1, y1);
    ctx.lineTo(x2, y2);
    ctx.stroke();
}

function drawShape(mode, startX, startY, endX, endY, color, width) {
    ctx.strokeStyle = color;
    ctx.lineWidth = width;
    ctx.lineCap = 'round';
    ctx.lineJoin = 'round';

    if (mode === 'line') {
        ctx.beginPath();
        ctx.moveTo(startX, startY);
        ctx.lineTo(endX, endY);
        ctx.stroke();
    } else if (mode === 'rectangle') {
        ctx.beginPath();
        ctx.rect(startX, startY, endX - startX, endY - startY);
        ctx.stroke();
    } else if (mode === 'circle') {
        const radius = Math.sqrt(Math.pow(endX - startX, 2) + Math.pow(endY - startY, 2));
        ctx.beginPath();
        ctx.arc(startX, startY, radius, 0, 2 * Math.PI);
        ctx.stroke();
    }
}

function drawText(text, x, y, color, size) {
    ctx.fillStyle = color;
    ctx.font = `${size * 4}px sans-serif`;
    ctx.fillText(text, x, y);
}

function getMousePos(e) {
    const rect = canvas.getBoundingClientRect();
    const scaleX = canvas.width / rect.width;
    const scaleY = canvas.height / rect.height;

    return {
        x: (e.clientX - rect.left) * scaleX,
        y: (e.clientY - rect.top) * scaleY
    };
}

function getTouchPos(e) {
    const rect = canvas.getBoundingClientRect();
    const scaleX = canvas.width / rect.width;
    const scaleY = canvas.height / rect.height;
    const touch = e.touches[0];

    return {
        x: (touch.clientX - rect.left) * scaleX,
        y: (touch.clientY - rect.top) * scaleY
    };
}

// Mouse events
canvas.addEventListener('mousedown', (e) => {
    const pos = getMousePos(e);

    if (currentMode === 'text') {
        const text = prompt('Enter text:');
        if (text) {
            const color = colorPicker.value;
            const width = parseInt(widthSlider.value);

            // Optimistic: draw locally first
            drawText(text, pos.x, pos.y, color, width);

            const msg = {
                type: 'text',
                text: text,
                x: pos.x,
                y: pos.y,
                color: color,
                width: width
            };

            if (ws && ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify(msg));
            }
        }
        return;
    }

    isDrawing = true;
    startX = pos.x;
    startY = pos.y;
    lastX = pos.x;
    lastY = pos.y;

    // Save canvas state for shape preview
    if (currentMode !== 'pen' && currentMode !== 'eraser') {
        snapshot = ctx.getImageData(0, 0, canvas.width, canvas.height);
    }
});

canvas.addEventListener('mousemove', (e) => {
    if (!isDrawing) return;

    const pos = getMousePos(e);
    const color = colorPicker.value;
    const width = parseInt(widthSlider.value);

    if (currentMode === 'pen' || currentMode === 'eraser') {
        // Pen/Eraser mode: continuous drawing
        const drawColor = currentMode === 'eraser' ? '#ffffff' : color;
        const drawWidth = currentMode === 'eraser' ? width * 3 : width;

        drawLine(lastX, lastY, pos.x, pos.y, drawColor, drawWidth);

        const msg = {
            type: 'draw',
            prevX: lastX,
            prevY: lastY,
            x: pos.x,
            y: pos.y,
            color: drawColor,
            width: drawWidth
        };

        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify(msg));
        }

        lastX = pos.x;
        lastY = pos.y;
    } else {
        // Shape mode: preview while dragging
        ctx.putImageData(snapshot, 0, 0);
        drawShape(currentMode, startX, startY, pos.x, pos.y, color, width);
    }
});

canvas.addEventListener('mouseup', (e) => {
    if (!isDrawing) return;
    isDrawing = false;

    // Send final shape to server
    if (currentMode !== 'pen' && currentMode !== 'eraser') {
        const pos = getMousePos(e);
        const msg = {
            type: 'shape',
            mode: currentMode,
            startX: startX,
            startY: startY,
            endX: pos.x,
            endY: pos.y,
            color: colorPicker.value,
            width: parseInt(widthSlider.value)
        };

        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify(msg));
        }
    }
});

canvas.addEventListener('mouseout', () => {
    isDrawing = false;
});

// Touch events
canvas.addEventListener('touchstart', (e) => {
    e.preventDefault();
    const pos = getTouchPos(e);

    if (currentMode === 'text') {
        const text = prompt('Enter text:');
        if (text) {
            const color = colorPicker.value;
            const width = parseInt(widthSlider.value);

            drawText(text, pos.x, pos.y, color, width);

            const msg = {
                type: 'text',
                text: text,
                x: pos.x,
                y: pos.y,
                color: color,
                width: width
            };

            if (ws && ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify(msg));
            }
        }
        return;
    }

    isDrawing = true;
    startX = pos.x;
    startY = pos.y;
    lastX = pos.x;
    lastY = pos.y;

    if (currentMode !== 'pen' && currentMode !== 'eraser') {
        snapshot = ctx.getImageData(0, 0, canvas.width, canvas.height);
    }
});

canvas.addEventListener('touchmove', (e) => {
    e.preventDefault();
    if (!isDrawing) return;

    const pos = getTouchPos(e);
    const color = colorPicker.value;
    const width = parseInt(widthSlider.value);

    if (currentMode === 'pen' || currentMode === 'eraser') {
        const drawColor = currentMode === 'eraser' ? '#ffffff' : color;
        const drawWidth = currentMode === 'eraser' ? width * 3 : width;

        drawLine(lastX, lastY, pos.x, pos.y, drawColor, drawWidth);

        const msg = {
            type: 'draw',
            prevX: lastX,
            prevY: lastY,
            x: pos.x,
            y: pos.y,
            color: drawColor,
            width: drawWidth
        };

        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify(msg));
        }

        lastX = pos.x;
        lastY = pos.y;
    } else {
        ctx.putImageData(snapshot, 0, 0);
        drawShape(currentMode, startX, startY, pos.x, pos.y, color, width);
    }
});

canvas.addEventListener('touchend', (e) => {
    e.preventDefault();
    if (!isDrawing) return;
    isDrawing = false;

    // Send final shape to server
    if (currentMode !== 'pen' && currentMode !== 'eraser' && e.changedTouches.length > 0) {
        const touch = e.changedTouches[0];
        const rect = canvas.getBoundingClientRect();
        const scaleX = canvas.width / rect.width;
        const scaleY = canvas.height / rect.height;
        const endX = (touch.clientX - rect.left) * scaleX;
        const endY = (touch.clientY - rect.top) * scaleY;

        const msg = {
            type: 'shape',
            mode: currentMode,
            startX: startX,
            startY: startY,
            endX: endX,
            endY: endY,
            color: colorPicker.value,
            width: parseInt(widthSlider.value)
        };

        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify(msg));
        }
    }
});

// Clear button
clearBtn.addEventListener('click', () => {
    if (confirm('Clear the entire canvas? This will affect all users.')) {
        // Optimistic: clear locally first
        ctx.clearRect(0, 0, canvas.width, canvas.height);

        const msg = { type: 'clear' };
        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify(msg));
        }
    }
});

// Initialize
connect();
