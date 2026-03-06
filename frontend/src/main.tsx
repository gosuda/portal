import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App.tsx";
import "./index.css";
import { Ssgoi } from "@ssgoi/react";
import { hero } from "@ssgoi/react/view-transitions";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <Ssgoi
    config={{
      transitions: [
        {
          from: "/",
          to: "/server/*",
          transition: hero(),
          symmetric: true,
        },
      ],
    }}
  >
    {/* ⚠️ 중요: position: relative 필수! */}
    <div style={{ position: "relative", minHeight: "100vh" }}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </div>
  </Ssgoi>
);
