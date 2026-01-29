package main

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "math/big"
    "net/http"
    "os"
    "strconv"
    "time"
    _ "github.com/lib/pq"
)

// --- Configuration from Environment ---
var (
    db          *sql.DB
    dbUrl       = os.Getenv("DATABASE_URL")
    adminUser   = os.Getenv("ADMIN_USER")
    adminPass   = os.Getenv("ADMIN_PASS")
    ownerWallet = os.Getenv("OWNER_WALLET")
    feeRate     = 0.01 // 1%
)

func main() {
    // Connect to DB
    var err error
    db, err = sql.Open("postgres", dbUrl)
    if err != nil {
        log.Fatal("Database Connection Error:", err)
    }
    defer db.Close()

    // Ping to ensure connection
    if err = db.Ping(); err != nil {
        log.Fatal("Database Ping Error:", err)
    }
    log.Println("System Online: Database Connected.")

    // Initialize Schema
    initSchema()

    // Routes (API + Frontend)
    http.HandleFunc("/", indexHandler)
    http.HandleFunc("/api/deposit", handleDeposit)
    http.HandleFunc("/api/createJob", handleCreateJob)
    http.HandleFunc("/api/admin/activity", adminAuth(handleAdminActivity))

    // Start Engine
    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    fmt.Printf("LuxCompute Engine Running on Port %s...\n", port)
    log.Fatal(http.ListenAndServe(":"+port, nil))
}

// --- Backend Logic ---

func initSchema() {
    query := `
    CREATE TABLE IF NOT EXISTS agents (
        id SERIAL PRIMARY KEY,
        wallet TEXT UNIQUE,
        balance BIGINT DEFAULT 0
    );
    CREATE TABLE IF NOT EXISTS activity_log (
        id SERIAL PRIMARY KEY,
        description TEXT,
        timestamp TIMESTAMP DEFAULT NOW()
    );
    `
    _, err := db.Exec(query)
    if err != nil {
        log.Println("Schema Init Warning:", err)
    }
}

func logActivity(desc string) {
    _, err := db.Exec("INSERT INTO activity_log (description) VALUES ($1)", desc)
    if err != nil {
        log.Println("Logging Error:", err)
    }
}

func handleDeposit(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        return
    }
    var req struct{ Wallet string `json:"wallet"` }
    json.NewDecoder(r.Body).Decode(&req)

    // Add 1 ETH (Mock logic for testing - in real prod, you'd verify on-chain)
    // 1 ETH = 10^18 Wei
    depositAmount := big.NewInt(0).Exp(big.NewInt(10), big.NewInt(18), nil)

    _, err := db.Exec(`
        INSERT INTO agents (wallet, balance) 
        VALUES ($1, $2) 
        ON CONFLICT (wallet) 
        DO UPDATE SET balance = agents.balance + $2
    `, req.Wallet, depositAmount.String())

    if err != nil {
        http.Error(w, "DB Error", 500)
        return
    }

    logActivity(fmt.Sprintf("DEPOSIT: %s received 1 ETH", req.Wallet))
    w.WriteHeader(http.StatusOK)
}

func handleCreateJob(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        return
    }
    var req struct{ Wallet string `json:"wallet"` }
    json.NewDecoder(r.Body).Decode(&req)

    // Cost: 0.01 ETH
    jobCost := big.NewInt(0).Exp(big.NewInt(10), big.NewInt(16), nil) // 10^16
    
    // Check Balance
    var currentBal int64
    err := db.QueryRow("SELECT balance FROM agents WHERE wallet = $1", req.Wallet).Scan(&currentBal)
    if err != nil || currentBal < jobCost.Int64() {
        http.Error(w, "Insufficient Funds", 400)
        return
    }

    // Calculate Fee
    feeAmount := new(big.Int)
    feeAmount.Set(jobCost)
    feeAmount.Mul(feeAmount, big.NewInt(100)) // x 100 to avoid float precision loss
    feeAmount.Div(feeAmount, big.NewInt(10000)) // / 10000 to get 1% (1.00%)

    // Deduct Balance
    tx, _ := db.Begin()
    tx.Exec("UPDATE agents SET balance = balance - $1 WHERE wallet = $2", jobCost.Int64(), req.Wallet)
    
    // Log Fee
    logActivity(fmt.Sprintf("JOB CREATED: Fee %s Wei deducted to %s", feeAmount.String(), ownerWallet))
    
    tx.Commit()

    logActivity(fmt.Sprintf("JOB EXECUTION STARTED by %s", req.Wallet))
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"status": "success", "fee_wei": feeAmount.String()})
}

func adminAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        u, p, ok := r.BasicAuth()
        if !ok || u != adminUser || p != adminPass {
            w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
            http.Error(w, "Unauthorized", 401)
            return
        }
        next(w, r)
    }
}

func handleAdminActivity(w http.ResponseWriter, r *http.Request) {
    rows, _ := db.Query("SELECT description, timestamp FROM activity_log ORDER BY id DESC LIMIT 20")
    defer rows.Close()
    
    var logs []map[string]interface{}
    for rows.Next() {
        var desc string
        var ts time.Time
        rows.Scan(&desc, &ts)
        logs = append(logs, map[string]interface{}{
            "desc": desc,
            "time": ts.Format("15:04:05"),
        })
    }
    json.NewEncoder(w).Encode(logs)
}

// --- Frontend (Embedded in Go) ---
func indexHandler(w http.ResponseWriter, r *http.Request) {
    html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>LuxCompute | Infrastructure</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <link href="https://fonts.googleapis.com/css2?family=Orbitron:wght@400;700;900&family=Rajdhani:wght@300;500;700&display=swap" rel="stylesheet">
    <style>
        :root {
            --neon-blue: #00f3ff;
            --neon-purple: #bc13fe;
            --bg-dark: #050510;
        }
        body {
            font-family: 'Rajdhani', sans-serif;
            background-color: var(--bg-dark);
            color: white;
            overflow-x: hidden;
        }
        .font-orbitron { font-family: 'Orbitron', sans-serif; }
        
        /* Effects */
        .watermark {
            position: fixed; top: 50%; left: 50%; transform: translate(-50%, -50%);
            width: 500px; opacity: 0.04; pointer-events: none; z-index: 0;
        }
        @keyframes scanline {
            0% { transform: translateY(-100%); } 100% { transform: translateY(100vh); }
        }
        .scanline {
            position: fixed; top: 0; left: 0; width: 100%; height: 2px;
            background: rgba(0, 243, 255, 0.4);
            animation: scanline 6s linear infinite; pointer-events: none; z-index: 50;
        }
        .glass-panel {
            background: rgba(16, 20, 30, 0.8);
            backdrop-filter: blur(12px);
            border: 1px solid rgba(0, 243, 255, 0.15);
            box-shadow: 0 0 30px rgba(0, 243, 255, 0.05);
        }
        .btn-neon {
            background: rgba(0, 243, 255, 0.05); border: 1px solid var(--neon-blue);
            color: var(--neon-blue); text-transform: uppercase; letter-spacing: 2px;
            transition: 0.3s; position: relative; overflow: hidden;
        }
        .btn-neon:hover {
            background: var(--neon-blue); color: black; box-shadow: 0 0 20px var(--neon-blue);
        }
        .secret-trigger {
            width: 12px; height: 12px; background: #0f0f0f; border-radius: 50%;
            position: fixed; bottom: 30px; right: 30px; cursor: pointer;
            border: 1px solid #333; z-index: 100;
        }
        .secret-trigger:hover { background: var(--neon-blue); box-shadow: 0 0 10px var(--neon-blue); }
    </style>
</head>
<body>
    <div class="scanline"></div>
    <div class="watermark">
        <svg viewBox="0 0 200 200" fill="none" xmlns="http://www.w3.org/2000/svg">
            <rect x="40" y="40" width="120" height="120" stroke="white" stroke-width="8"/>
            <path d="M60 60 L140 60 L140 140 L60 140 Z" fill="black" />
            <text x="100" y="110" font-family="Orbitron" font-size="40" fill="white" text-anchor="middle">LUX</text>
        </svg>
    </div>

    <!-- Phase 1: Welcome -->
    <div id="welcome-screen" class="flex flex-col items-center justify-center min-h-screen z-10 relative">
        <h1 class="font-orbitron text-7xl md:text-9xl bg-clip-text text-transparent bg-gradient-to-r from-cyan-400 via-blue-500 to-purple-600 mb-4" style="filter: drop-shadow(0 0 20px rgba(0,243,255,0.4));">
            LUX<span class="text-white">COMPUTE</span>
        </h1>
        <p class="text-gray-400 text-2xl tracking-[0.3em] uppercase mb-12 font-light">Decentralized Compute Layer</p>
        <button onclick="enterDashboard()" class="btn-neon px-12 py-4 rounded text-xl font-bold">
            Initialize Protocol
        </button>
    </div>

    <!-- Phase 2: Dashboard -->
    <div id="dashboard" class="hidden min-h-screen p-6 md:p-12 z-10 relative fade-in">
        <header class="flex justify-between items-center mb-16 border-b border-gray-800 pb-6">
            <div class="flex items-center gap-4">
                <div class="w-10 h-10 bg-cyan-500/10 rounded flex items-center justify-center border border-cyan-500/50">
                    <div class="w-4 h-4 bg-cyan-400 rounded-full animate-pulse"></div>
                </div>
                <span class="font-orbitron text-2xl font-bold tracking-wider">LUX<span class="text-cyan-400">COMPUTE</span></span>
            </div>
            <div class="hidden md:block text-right">
                <p class="text-xs text-gray-500 uppercase tracking-widest">System Status</p>
                <p class="text-cyan-400 font-mono">OPERATIONAL</p>
            </div>
        </header>

        <div class="grid grid-cols-1 lg:grid-cols-3 gap-8">
            <!-- Agent Module -->
            <div class="glass-panel p-8 rounded-2xl col-span-1">
                <h2 class="font-orbitron text-xl text-gray-300 mb-6 border-l-4 border-cyan-500 pl-4">WALLET NODE</h2>
                <div class="mb-8">
                    <p class="text-gray-500 text-xs uppercase mb-2">Connected Identity</p>
                    <p class="text-cyan-400 font-mono text-sm truncate bg-black/30 p-2 rounded" id="wallet-display">0x...</p>
                </div>
                <div class="mb-8">
                    <p class="text-gray-500 text-xs uppercase mb-2">Available Credits</p>
                    <p class="text-4xl font-bold text-white tracking-tighter" id="balance-display">0.0000</p>
                </div>
                <div class="space-y-3">
                    <button onclick="deposit()" class="w-full btn-neon py-3 rounded font-bold">DEPOSIT TEST FUNDS</button>
                    <button onclick="runJob()" class="w-full btn-neon py-3 rounded font-bold border-purple-500 text-purple-400 hover:bg-purple-500 hover:text-white hover:border-purple-500 hover:shadow-[0_0_15px_#bc13fe]">EXECUTE JOB</button>
                </div>
            </div>

            <!-- Metrics -->
            <div class="glass-panel p-8 rounded-2xl col-span-1 lg:col-span-2">
                <h2 class="font-orbitron text-xl text-gray-300 mb-6 border-l-4 border-purple-500 pl-4">NETWORK METRICS</h2>
                <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
                    <div class="bg-black/40 p-6 rounded-xl border border-gray-800 hover:border-gray-600 transition">
                        <p class="text-gray-500 text-xs uppercase mb-1">Nodes</p>
                        <p class="text-3xl font-orbitron text-cyan-400">1,024</p>
                    </div>
                    <div class="bg-black/40 p-6 rounded-xl border border-gray-800 hover:border-gray-600 transition">
                        <p class="text-gray-500 text-xs uppercase mb-1">Latency</p>
                        <p class="text-3xl font-orbitron text-green-400">12ms</p>
                    </div>
                    <div class="bg-black/40 p-6 rounded-xl border border-gray-800 hover:border-gray-600 transition">
                        <p class="text-gray-500 text-xs uppercase mb-1">Fee</p>
                        <p class="text-3xl font-orbitron text-purple-400">1.0%</p>
                    </div>
                    <div class="bg-black/40 p-6 rounded-xl border border-gray-800 hover:border-gray-600 transition">
                        <p class="text-gray-500 text-xs uppercase mb-1">Security</p>
                        <p class="text-3xl font-orbitron text-white">AES-256</p>
                    </div>
                </div>
                <div class="mt-8 p-4 bg-black/20 rounded border border-gray-800">
                    <p class="text-gray-400 text-xs mb-2">RECENT TRANSACTION HASH</p>
                    <p class="font-mono text-gray-600 text-xs truncate" id="hash-display">Awaiting request...</p>
                </div>
            </div>
        </div>
    </div>

    <!-- Admin Panel -->
    <div id="admin-panel" class="hidden fixed inset-0 bg-black/95 z-[100] flex items-center justify-center p-8 backdrop-blur-md">
        <div class="w-full max-w-5xl glass-panel p-1 rounded-xl border border-red-500/30 shadow-[0_0_100px_rgba(220,38,38,0.1)]">
            <div class="bg-black/50 p-8 rounded-lg">
                <div class="flex justify-between items-start mb-8 border-b border-gray-800 pb-4">
                    <div>
                        <h1 class="font-orbitron text-4xl text-red-500 tracking-widest">SYSTEM OVERRIDE</h1>
                        <p class="text-gray-500 text-sm mt-1 font-mono">ADMINISTRATIVE ACCESS GRANTED</p>
                    </div>
                    <button onclick="closeAdmin()" class="text-gray-500 hover:text-white font-orbitron transition">[CLOSE]</button>
                </div>
                
                <div class="grid grid-cols-3 gap-6 mb-8">
                    <div class="bg-red-500/10 p-4 rounded border border-red-500/20">
                        <p class="text-xs text-red-400 uppercase">Fees Collected (Wei)</p>
                        <p class="text-xl font-bold text-white mt-1" id="fee-counter">0</p>
                    </div>
                    <div class="bg-blue-500/10 p-4 rounded border border-blue-500/20">
                        <p class="text-xs text-blue-400 uppercase">Active Agents</p>
                        <p class="text-xl font-bold text-white mt-1">Scanning...</p>
                    </div>
                    <div class="bg-green-500/10 p-4 rounded border border-green-500/20">
                        <p class="text-xs text-green-400 uppercase">System Integrity</p>
                        <p class="text-xl font-bold text-white mt-1">100%</p>
                    </div>
                </div>

                <h3 class="text-gray-300 mb-4 font-orbitron">ACTIVITY STREAM</h3>
                <div id="admin-logs" class="h-64 overflow-y-auto bg-black/80 p-4 font-mono text-xs text-gray-400 border border-gray-700 rounded mb-4">
                    <p>Connecting to secure database...</p>
                </div>
            </div>
        </div>
    </div>

    <div class="secret-trigger" onclick="adminLogin()"></div>

    <script>
        let wallet = null;
        
        function enterDashboard() {
            document.getElementById('welcome-screen').classList.add('hidden');
            document.getElementById('dashboard').classList.remove('hidden');
            // Simulate Connect
            wallet = '0x71C7656EC7ab88b098defB751B7401B5f6d8976F';
            document.getElementById('wallet-display').innerText = wallet;
        }

        async function deposit() {
            try {
                const res = await fetch('/api/deposit', {
                    method: 'POST',
                    body: JSON.stringify({ wallet: wallet })
                });
                if(res.ok) {
                    alert("Deposit Successful: 1 ETH Added");
                    document.getElementById('hash-display').innerText = "0xSimulatedDeposit..." + Date.now();
                }
            } catch(e) { alert("Error"); }
        }

        async function runJob() {
            try {
                const res = await fetch('/api/createJob', {
                    method: 'POST',
                    body: JSON.stringify({ wallet: wallet })
                });
                const data = await res.json();
                alert("Job Submitted. Fee Deducted: " + data.fee_wei + " Wei");
                document.getElementById('hash-display').innerText = "0xJobExec..." + Date.now();
            } catch(e) { alert("Error"); }
        }

        function adminLogin() {
            const u = prompt("IDENTITY:");
            const p = prompt("PASSCODE:");
            if(u === "` + adminUser + `" && p === "` + adminPass + `") {
                fetchLogs();
                document.getElementById('admin-panel').classList.remove('hidden');
            } else {
                alert("ACCESS DENIED");
            }
        }

        function closeAdmin() {
            document.getElementById('admin-panel').classList.add('hidden');
        }

        async function fetchLogs() {
            try {
                const res = await fetch('/api/admin/activity', {
                    headers: { 'Authorization': 'Basic ' + btoa('` + adminUser + `:` + adminPass + `') }
                });
                const logs = await res.json();
                const div = document.getElementById('admin-logs');
                div.innerHTML = "";
                let fees = 0;
                logs.forEach(l => {
                    const row = `<div class="mb-1 border-b border-gray-800 pb-1 flex justify-between"><span>[${l.time}] ${l.desc}</span></div>`;
                    div.innerHTML += row;
                    if(l.desc.includes("Fee")) fees++; 
                });
                document.getElementById('fee-counter').innerText = fees + " Transactions";
            } catch(e) { console.log(e); }
        }
    </script>
</body>
</html>`
    w.Header().Set("Content-Type", "text/html")
    w.Write([]byte(html))
}