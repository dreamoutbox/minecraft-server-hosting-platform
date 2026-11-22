package domain

import (
	"os"
	"testing"
)

// getTestDSN returns the PostgreSQL connection string for integration testing.
func getTestDSN() string {
	if dsn := os.Getenv("TEST_DATABASE_DSN"); dsn != "" {
		return dsn
	}
	return "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
}

func TestDatabaseMigrationAndSeed(t *testing.T) {
	dsn := getTestDSN()
	db, err := InitDB(dsn)
	if err != nil {
		t.Skipf("skipping postgres test (database unreachable: %v)", err)
		return
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	if err := SeedPlans(db); err != nil {
		t.Fatalf("SeedPlans failed: %v", err)
	}

	// Verify idempotency
	if err := SeedPlans(db); err != nil {
		t.Fatalf("SeedPlans idempotent run failed: %v", err)
	}

	var plans []Plan
	if err := db.Find(&plans).Error; err != nil {
		t.Fatalf("failed fetching plans: %v", err)
	}

	if len(plans) < 3 {
		t.Fatalf("expected at least 3 plans seeded, got %d", len(plans))
	}

	expectedMap := map[string]struct {
		cpu  int
		mem  int
		disk int
	}{
		"Small":  {cpu: 1, mem: 1024, disk: 10},
		"Medium": {cpu: 2, mem: 2048, disk: 20},
		"Large":  {cpu: 4, mem: 4096, disk: 40},
	}

	for _, p := range plans {
		if expected, ok := expectedMap[p.Name]; ok {
			if p.CPULimit != expected.cpu || p.MemLimitMB != expected.mem || p.DiskLimitGB != expected.disk {
				t.Errorf("plan %s mismatch: got cpu=%d mem=%d disk=%d, expected %+v",
					p.Name, p.CPULimit, p.MemLimitMB, p.DiskLimitGB, expected)
			}
		}
	}
}

func TestServerPortUniqueConstraint(t *testing.T) {
	dsn := getTestDSN()
	db, err := InitDB(dsn)
	if err != nil {
		t.Skipf("skipping postgres test (database unreachable: %v)", err)
		return
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	user := User{Email: "test-port@example.com", PasswordHash: "hashed", Role: UserRoleUser}
	_ = db.Where("email = ?", user.Email).FirstOrCreate(&user)

	host := Host{
		Hostname:      "node-1.internal",
		WorkerBaseURL: "http://10.0.0.1:9090",
		APIKeyHash:    "dummyhash",
		MaxServers:    10,
		Status:        HostStatusActive,
	}
	_ = db.Where("hostname = ?", host.Hostname).FirstOrCreate(&host)

	var plan Plan
	if err := db.First(&plan).Error; err != nil {
		t.Fatalf("no plan found: %v", err)
	}

	s1 := Server{
		UserID: user.ID,
		PlanID: plan.ID,
		HostID: host.ID,
		Name:   "server-alpha",
		Port:   25599,
		Status: ServerStatusRunning,
	}

	// Clean up any pre-existing server on 25599 from previous test run
	db.Where("port = ?", 25599).Delete(&Server{})

	if err := db.Create(&s1).Error; err != nil {
		t.Fatalf("failed inserting first server: %v", err)
	}

	s2 := Server{
		UserID: user.ID,
		PlanID: plan.ID,
		HostID: host.ID,
		Name:   "server-beta",
		Port:   25599, // duplicate port
		Status: ServerStatusRunning,
	}

	if err := db.Create(&s2).Error; err == nil {
		t.Fatal("expected unique constraint error for duplicate port, got nil")
	}
}

func TestSeedDefaultHost(t *testing.T) {
	dsn := getTestDSN()
	db, err := InitDB(dsn)
	if err != nil {
		t.Skipf("skipping postgres test (database unreachable: %v)", err)
		return
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	testURL := "http://test-worker-node:9090"
	testFSURL := "http://test-worker-node:9100"
	testDomain := "play.test-node.example.com"
	testIP := "203.0.113.50"
	if err := SeedDefaultHost(db, "test-worker", testURL, testFSURL, testDomain, testIP, "test-key-123", 20); err != nil {
		t.Fatalf("SeedDefaultHost failed: %v", err)
	}

	var host Host
	if err := db.Where("worker_base_url = ?", testURL).First(&host).Error; err != nil {
		t.Fatalf("failed finding seeded host: %v", err)
	}
	if host.Status != HostStatusActive {
		t.Errorf("expected host active, got: %v", host.Status)
	}
	if host.FileServerBaseURL != testFSURL {
		t.Errorf("expected fileserver url %s, got: %s", testFSURL, host.FileServerBaseURL)
	}
	if host.PublicDomain != testDomain {
		t.Errorf("expected public domain %s, got: %s", testDomain, host.PublicDomain)
	}
	if host.PublicIP != testIP {
		t.Errorf("expected public ip %s, got: %s", testIP, host.PublicIP)
	}

	// Test idempotency
	if err := SeedDefaultHost(db, "test-worker", testURL, testFSURL, testDomain, testIP, "new-test-key", 20); err != nil {
		t.Fatalf("SeedDefaultHost idempotent run failed: %v", err)
	}
	_ = db.Where("worker_base_url = ?", testURL).First(&host)
	if host.APIKey != "new-test-key" {
		t.Errorf("expected updated api key new-test-key, got: %s", host.APIKey)
	}
	// Clean up test host
	db.Delete(&host)
}

func TestServerConnectAddress(t *testing.T) {
	cases := []struct {
		name string
		srv  Server
		want string
	}{
		{
			name: "prefers public domain",
			srv:  Server{Port: 25565, Host: Host{Hostname: "node-1", PublicDomain: "mc1.example.com", PublicIP: "203.0.113.10"}},
			want: "mc1.example.com:25565",
		},
		{
			name: "falls back to public ip",
			srv:  Server{Port: 25566, Host: Host{Hostname: "node-1", PublicIP: "203.0.113.10"}},
			want: "203.0.113.10:25566",
		},
		{
			name: "falls back to hostname",
			srv:  Server{Port: 25567, Host: Host{Hostname: "node-1"}},
			want: "node-1:25567",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.srv.ConnectAddress(); got != tc.want {
				t.Errorf("ConnectAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSeedDefaultHostUpdatesExistingHost(t *testing.T) {
	dsn := getTestDSN()
	db, err := InitDB(dsn)
	if err != nil {
		t.Skipf("skipping postgres test (database unreachable: %v)", err)
		return
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	hostname := "test-seed-worker-update"
	db.Where("hostname = ?", hostname).Delete(&Host{})
	t.Cleanup(func() {
		db.Where("hostname = ?", hostname).Delete(&Host{})
	})

	if err := SeedDefaultHost(db, hostname, "http://worker-old:9090", "http://fs-old:9100", "old.example.com", "10.0.0.1", "old-key", 10); err != nil {
		t.Fatalf("initial SeedDefaultHost failed: %v", err)
	}

	if err := SeedDefaultHost(db, hostname, "http://worker-new:9090", "http://fs-new:9100", "new.example.com", "10.0.0.2", "new-key", 35); err != nil {
		t.Fatalf("update SeedDefaultHost failed: %v", err)
	}

	var updated Host
	if err := db.Where("hostname = ?", hostname).First(&updated).Error; err != nil {
		t.Fatalf("failed reloading updated host: %v", err)
	}
	if updated.WorkerBaseURL != "http://worker-new:9090" ||
		updated.FileServerBaseURL != "http://fs-new:9100" ||
		updated.PublicDomain != "new.example.com" ||
		updated.PublicIP != "10.0.0.2" ||
		updated.APIKey != "new-key" ||
		updated.MaxServers != 35 {
		t.Errorf("SeedDefaultHost did not update all fields: %+v", updated)
	}
}

func TestServerImageBackupPartKeysHelpers(t *testing.T) {
	var rec ServerImageBackup
	want := []string{
		"image-backups/server-1/100/part-001.tar.gz",
		"image-backups/server-1/100/part-002.tar.gz",
	}
	if err := rec.SetPartKeys(want); err != nil {
		t.Fatalf("SetPartKeys failed: %v", err)
	}
	if rec.PartCount != len(want) {
		t.Fatalf("PartCount = %d, want %d", rec.PartCount, len(want))
	}
	got, err := rec.GetPartKeys()
	if err != nil {
		t.Fatalf("GetPartKeys failed: %v", err)
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("GetPartKeys = %v, want %v", got, want)
	}

	empty := ServerImageBackup{}
	keys, err := empty.GetPartKeys()
	if err != nil {
		t.Fatalf("GetPartKeys on empty failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty keys, got %v", keys)
	}
}
