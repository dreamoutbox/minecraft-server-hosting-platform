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
