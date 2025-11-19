const puppeteer = require('puppeteer');
const fs = require('fs');
const path = require('path');

(async () => {
    const targetUrl = process.env.TARGET_URL || 'http://localhost:4017';
    const screenshotDir = path.join(__dirname, '..', '..', '..', 'artifacts', 'screenshots');
    console.log(`Target URL: ${targetUrl}`);
    console.log(`Screenshot directory: ${screenshotDir}`);

    // Ensure screenshot directory exists
    if (!fs.existsSync(screenshotDir)) {
        fs.mkdirSync(screenshotDir, { recursive: true });
        console.log('Created screenshot directory:', screenshotDir);
    }

    const browser = await puppeteer.launch({
        headless: "new",
        args: ['--no-sandbox', '--disable-setuid-sandbox']
    });
    const page = await browser.newPage();

    try {
        // 1. Navigate to the page and wait for auto-reload
        console.log('Navigating to page and waiting for auto-reload...');
        await page.goto(targetUrl, { waitUntil: 'networkidle0', timeout: 30000 });
        console.log('Initial page loaded, waiting for potential auto-reload...');

        // Wait for a few seconds to catch any auto-reload
        await new Promise(resolve => setTimeout(resolve, 5000));
        
        // Check if the page has reloaded by comparing URLs or content
        const currentUrl = page.url();
        console.log(`Current URL after wait: ${currentUrl}`);
        if (currentUrl !== targetUrl) {
            console.log('Page reloaded to a new URL.');
        } else {
            console.log('Page did not reload.');
        }
        
        // Capture screenshot of the initial loaded state (Portal UI)
        await page.screenshot({ path: path.join(screenshotDir, '01-portal-ui.png'), fullPage: true });
        console.log('Captured screenshot: 01-portal-ui.png');

        // 2. Verify content and capture "Test App Initial Load"
        console.log('Verifying content...');
        const content = await page.content();
        console.log('Page content length:', content.length);
        if (!content.includes('Hello World')) {
            console.error('Page content:', content);
            throw new Error('Page content does not contain "Hello World"');
        }
        console.log('Content verification passed.');

        // Capture screenshot of the Test App Initial Load
        await page.screenshot({ path: path.join(screenshotDir, '02-test-app-initial-load.png'), fullPage: true });
        console.log('Captured screenshot: 02-test-app-initial-load.png');

        // 3. Capture "Connection Screen" before WebSocket tests
        // Assuming the connection screen is visible before WebSocket interaction
        await page.screenshot({ path: path.join(screenshotDir, '03-connection-screen.png'), fullPage: true });
        console.log('Captured screenshot: 03-connection-screen.png');

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
        
        // 4. Capture final state after tests
        await page.screenshot({ path: path.join(screenshotDir, '04-final-state.png'), fullPage: true });
        console.log('Captured screenshot: 04-final-state.png');

    } catch (error) {
        console.error('Test failed:', error);
        // Capture screenshot on failure for debugging
        await page.screenshot({ path: path.join(screenshotDir, '00-error-state.png'), fullPage: true });
        console.log('Captured screenshot on failure: 00-error-state.png');
        process.exit(1);
    } finally {
        await browser.close();
    }
})();
