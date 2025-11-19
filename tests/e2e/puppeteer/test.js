const puppeteer = require('puppeteer');

(async () => {
    const targetUrl = process.env.TARGET_URL || 'http://localhost:4017';
    console.log(`Target URL: ${targetUrl}`);

    const browser = await puppeteer.launch({
        headless: "new",
        args: ['--no-sandbox', '--disable-setuid-sandbox']
    });
    const page = await browser.newPage();

    try {
        // 1. Navigate to the page
        console.log('Navigating to page...');
        await page.goto(targetUrl, { waitUntil: 'networkidle0', timeout: 30000 });
        console.log('Page loaded');

        // 2. Verify content
        console.log('Verifying content...');
        const content = await page.content();
        console.log('Page content length:', content.length);
        if (!content.includes('Hello World')) {
            console.error('Page content:', content);
            throw new Error('Page content does not contain "Hello World"');
        }
        console.log('Content verification passed.');

        // 3. Test WebSocket
        console.log('Testing WebSocket...');
        await page.evaluate(async () => {
            const getWsUrl = () => {
                let wsUrl = window.location.origin.replace(/^http/, 'ws');
                if (wsUrl.endsWith('/')) {
                    wsUrl += 'ws';
                } else {
                    wsUrl += '/ws';
                }
                return wsUrl;
            };

            const connect = () => {
                return new Promise((resolve, reject) => {
                    const ws = new WebSocket(getWsUrl());
                    ws.onopen = () => {
                        console.log('WebSocket connected');
                        ws.send('ping');
                    };
                    ws.onmessage = (event) => {
                        console.log('WebSocket message received:', event.data);
                        if (event.data === 'ping') {
                            ws.close();
                            resolve();
                        } else {
                            reject(new Error('Unexpected message: ' + event.data));
                        }
                    };
                    ws.onerror = (event) => {
                        console.error('WebSocket error event:', event);
                        reject(new Error('WebSocket connection failed'));
                    };
                });
            };

            // Test 1: Initial Connection
            console.log('Test 1: Initial Connection');
            await connect();
            console.log('Test 1 Passed');

            // Test 2: Reconnection
            console.log('Test 2: Reconnection');
            await new Promise(r => setTimeout(r, 1000)); // Wait a bit
            await connect();
            console.log('Test 2 Passed');

            // Test 3: Error Handling (Invalid URL)
            console.log('Test 3: Error Handling');
            await new Promise((resolve, reject) => {
                const ws = new WebSocket(getWsUrl() + '_invalid');
                ws.onopen = () => {
                    ws.close();
                    reject(new Error('Should have failed to connect to invalid URL'));
                };
                ws.onerror = () => {
                    console.log('Got expected error for invalid URL');
                    resolve();
                };
            });
            console.log('Test 3 Passed');
        });
        console.log('WebSocket tests passed.');

    } catch (error) {
        console.error('Test failed:', error);
        process.exit(1);
    } finally {
        await browser.close();
    }
})();
